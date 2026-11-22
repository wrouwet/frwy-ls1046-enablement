package label

import (
	"encoding/binary"
	"net/netip"
	"strings"
)

// DNSAnswers parses a DNS response and returns the A/AAAA addresses mapped to
// the name the client originally asked for (the question name, so CNAME
// chains such as www.youtube.com -> youtube-ui.l.google.com stay attributed
// to youtube.com).
func DNSAnswers(m []byte) (qname string, addrs []netip.Addr) {
	if len(m) < 12 || m[2]&0x80 == 0 { // not a response
		return "", nil
	}
	qd := int(binary.BigEndian.Uint16(m[4:]))
	an := int(binary.BigEndian.Uint16(m[6:]))
	off := 12
	for i := 0; i < qd; i++ {
		name, n := readName(m, off)
		if n < 0 || off+n+4 > len(m) {
			return "", nil
		}
		if i == 0 {
			qname = name
		}
		off += n + 4
	}
	for i := 0; i < an; i++ {
		_, n := readName(m, off)
		if n < 0 || off+n+10 > len(m) {
			break
		}
		off += n
		typ := binary.BigEndian.Uint16(m[off:])
		rdl := int(binary.BigEndian.Uint16(m[off+8:]))
		off += 10
		if off+rdl > len(m) {
			break
		}
		switch {
		case typ == 1 && rdl == 4:
			addrs = append(addrs, netip.AddrFrom4([4]byte(m[off:off+4])))
		case typ == 28 && rdl == 16:
			addrs = append(addrs, netip.AddrFrom16([16]byte(m[off:off+16])))
		}
		off += rdl
	}
	return sanitizeName(qname), addrs
}

// readName returns the dotted name at off and the number of bytes it occupies
// at that position (compression pointers count as 2).
func readName(m []byte, off int) (string, int) {
	var parts []string
	consumed := -1
	pos := off
	for jumps := 0; jumps < 16; {
		if pos >= len(m) {
			return "", -1
		}
		l := int(m[pos])
		switch {
		case l == 0:
			if consumed < 0 {
				consumed = pos + 1 - off
			}
			return strings.Join(parts, "."), consumed
		case l&0xc0 == 0xc0:
			if pos+1 >= len(m) {
				return "", -1
			}
			if consumed < 0 {
				consumed = pos + 2 - off
			}
			pos = int(binary.BigEndian.Uint16(m[pos:]) & 0x3fff)
			jumps++
		default:
			if pos+1+l > len(m) {
				return "", -1
			}
			parts = append(parts, string(m[pos+1:pos+1+l]))
			pos += 1 + l
		}
	}
	return "", -1
}
