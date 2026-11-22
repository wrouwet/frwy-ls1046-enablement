package label

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"sort"
)

// QUIC Initial packets are encrypted with keys derived only from the
// client's Destination Connection ID and a public salt (RFC 9001 §5.2), so a
// passive observer can recover the TLS ClientHello and its SNI.

var quicV1Salt = []byte{0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a}

// QUIC v2 (RFC 9369) uses a different salt and labels.
var quicV2Salt = []byte{0x0d, 0xed, 0xe3, 0xde, 0xf7, 0x00, 0xa6, 0xdb, 0x81, 0x93,
	0x81, 0xbe, 0x6e, 0x26, 0x9d, 0xcb, 0xf9, 0xbd, 0x2e, 0xd9}

func hkdfExtract(salt, ikm []byte) []byte {
	m := hmac.New(sha256.New, salt)
	m.Write(ikm)
	return m.Sum(nil)
}

func hkdfExpand(prk, info []byte, n int) []byte {
	var out, t []byte
	var h hash.Hash
	for i := byte(1); len(out) < n; i++ {
		h = hmac.New(sha256.New, prk)
		h.Write(t)
		h.Write(info)
		h.Write([]byte{i})
		t = h.Sum(nil)
		out = append(out, t...)
	}
	return out[:n]
}

func hkdfExpandLabel(secret []byte, label string, n int) []byte {
	full := "tls13 " + label
	info := make([]byte, 0, 4+len(full))
	info = append(info, byte(n>>8), byte(n), byte(len(full)))
	info = append(info, full...)
	info = append(info, 0) // empty context
	return hkdfExpand(secret, info, n)
}

// InitialKeys holds the client-side Initial protection keys.
type InitialKeys struct {
	Secret, Key, IV, HP []byte
}

// DeriveClientInitial derives client Initial keys for QUIC v1 or v2.
func DeriveClientInitial(dcid []byte, version uint32) InitialKeys {
	salt, lk, li, lh := quicV1Salt, "quic key", "quic iv", "quic hp"
	if version == 0x6b3343cf {
		salt, lk, li, lh = quicV2Salt, "quicv2 key", "quicv2 iv", "quicv2 hp"
	}
	init := hkdfExtract(salt, dcid)
	cs := hkdfExpandLabel(init, "client in", 32)
	return InitialKeys{Secret: cs, Key: hkdfExpandLabel(cs, lk, 16), IV: hkdfExpandLabel(cs, li, 12), HP: hkdfExpandLabel(cs, lh, 16)}
}

func readVarint(b []byte) (uint64, int, bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	l := 1 << (b[0] >> 6)
	if len(b) < l {
		return 0, 0, false
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < l; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, l, true
}

// QUICInitialCrypto decrypts one QUIC long-header Initial packet sent by a
// client and returns its CRYPTO frame fragments (offset -> data) plus the
// DCID. Coalesced packets after the first are ignored.
func QUICInitialCrypto(p []byte) (dcid []byte, frags map[uint64][]byte, err error) {
	if len(p) < 7 || p[0]&0x80 == 0 {
		return nil, nil, errors.New("not long header")
	}
	version := binary.BigEndian.Uint32(p[1:])
	ptype := (p[0] >> 4) & 3
	isInitial := (version == 1 && ptype == 0) || (version == 0x6b3343cf && ptype == 1)
	if !isInitial {
		return nil, nil, errors.New("not initial")
	}
	off := 5
	dl := int(p[off])
	off++
	if off+dl > len(p) || dl > 20 {
		return nil, nil, errors.New("bad dcid")
	}
	dcid = append([]byte(nil), p[off:off+dl]...)
	off += dl
	if off >= len(p) {
		return nil, nil, errors.New("short")
	}
	sl := int(p[off])
	off += 1 + sl
	tl, n, ok := readVarint(p[off:]) // token length
	if !ok {
		return nil, nil, errors.New("bad token len")
	}
	off += n + int(tl)
	length, n, ok := readVarint(p[off:])
	if !ok {
		return nil, nil, errors.New("bad length")
	}
	off += n
	pnOff := off
	if pnOff+4+16 > len(p) || pnOff+int(length) > len(p) {
		return nil, nil, errors.New("truncated")
	}
	keys := DeriveClientInitial(dcid, version)
	blk, err := aes.NewCipher(keys.HP)
	if err != nil {
		return nil, nil, err
	}
	sample := p[pnOff+4 : pnOff+20]
	mask := make([]byte, 16)
	blk.Encrypt(mask, sample)
	hdr := append([]byte(nil), p[:pnOff+4]...)
	hdr[0] ^= mask[0] & 0x0f
	pnLen := int(hdr[0]&3) + 1
	for i := 0; i < pnLen; i++ {
		hdr[pnOff+i] ^= mask[1+i]
	}
	hdr = hdr[:pnOff+pnLen]
	var pn uint64
	for i := 0; i < pnLen; i++ {
		pn = pn<<8 | uint64(hdr[pnOff+i])
	}
	nonce := append([]byte(nil), keys.IV...)
	for i := 0; i < 8; i++ {
		nonce[11-i] ^= byte(pn >> (8 * i))
	}
	kb, err := aes.NewCipher(keys.Key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(kb)
	if err != nil {
		return nil, nil, err
	}
	ct := p[pnOff+pnLen : pnOff+int(length)]
	plain, err := aead.Open(nil, nonce, ct, hdr)
	if err != nil {
		return nil, nil, err
	}
	frags = map[uint64][]byte{}
	for i := 0; i < len(plain); {
		ft := plain[i]
		switch {
		case ft == 0x00 || ft == 0x01: // PADDING, PING
			i++
		case ft == 0x02 || ft == 0x03: // ACK: type, largest, delay, range count, first range, ranges...
			i++
			var vals [4]uint64
			for k := 0; k < 4; k++ {
				v, n, ok := readVarint(plain[i:])
				if !ok {
					return dcid, frags, nil
				}
				vals[k] = v
				i += n
			}
			for r := uint64(0); r < vals[2]*2; r++ {
				_, n, ok := readVarint(plain[i:])
				if !ok {
					return dcid, frags, nil
				}
				i += n
			}
			if ft == 0x03 {
				for k := 0; k < 3; k++ {
					_, n, ok := readVarint(plain[i:])
					if !ok {
						return dcid, frags, nil
					}
					i += n
				}
			}
		case ft == 0x06: // CRYPTO
			i++
			o, n, ok := readVarint(plain[i:])
			if !ok {
				return dcid, frags, nil
			}
			i += n
			l, n, ok := readVarint(plain[i:])
			if !ok || i+n+int(l) > len(plain) {
				return dcid, frags, nil
			}
			i += n
			frags[o] = append([]byte(nil), plain[i:i+int(l)]...)
			i += int(l)
		default: // CONNECTION_CLOSE or unknown: stop
			return dcid, frags, nil
		}
	}
	return dcid, frags, nil
}

// QUICAssembler collects CRYPTO fragments across a client's Initial packets
// (ClientHellos with post-quantum key shares span 2+ packets and browsers
// shuffle frame order) until the SNI can be read.
type QUICAssembler struct {
	frags map[uint64][]byte
	tries int
}

// Add feeds one UDP payload; returns the SNI once it is decodable.
func (a *QUICAssembler) Add(p []byte) (sni string, done bool) {
	a.tries++
	if a.tries > 6 {
		return "", true
	}
	_, fr, err := QUICInitialCrypto(p)
	if err != nil {
		return "", false
	}
	if a.frags == nil {
		a.frags = map[uint64][]byte{}
	}
	for o, d := range fr {
		a.frags[o] = d
	}
	offs := make([]uint64, 0, len(a.frags))
	for o := range a.frags {
		offs = append(offs, o)
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	var stream []byte
	for _, o := range offs {
		if o > uint64(len(stream)) {
			break
		}
		d := a.frags[o]
		if end := o + uint64(len(d)); end > uint64(len(stream)) {
			stream = append(stream, d[uint64(len(stream))-o:]...)
		}
	}
	if len(stream) >= 4 && stream[0] == 0x01 {
		need := 4 + (int(stream[1])<<16 | int(stream[2])<<8 | int(stream[3]))
		if len(stream) >= need || len(stream) > 1500 {
			if s := SNIFromClientHello(stream); s != "" {
				return s, true
			}
			if len(stream) >= need {
				return "", true
			}
		}
	}
	return "", false
}
