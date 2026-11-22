// Package label extracts the names that flows still leak in clear text
// (TLS/QUIC SNI, DNS answers, HTTP Host, proxy CONNECT) and maps them to
// traffic categories. These names are used to label training samples; the
// ML model then classifies flows where no name is visible.
package label

import "encoding/binary"

// SNIFromClientHello parses a TLS handshake message stream (starting at the
// handshake header, i.e. type 0x01) and returns the server_name extension.
func SNIFromClientHello(b []byte) string {
	if len(b) < 4 || b[0] != 0x01 {
		return ""
	}
	n := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	b = b[4:]
	if n < len(b) {
		b = b[:n]
	}
	// legacy_version(2) random(32)
	if len(b) < 34 {
		return ""
	}
	b = b[34:]
	// session id
	if len(b) < 1 || len(b) < 1+int(b[0]) {
		return ""
	}
	b = b[1+int(b[0]):]
	// cipher suites
	if len(b) < 2 {
		return ""
	}
	cs := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+cs {
		return ""
	}
	b = b[2+cs:]
	// compression methods
	if len(b) < 1 || len(b) < 1+int(b[0]) {
		return ""
	}
	b = b[1+int(b[0]):]
	if len(b) < 2 {
		return ""
	}
	el := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	if el < len(b) {
		b = b[:el]
	}
	for len(b) >= 4 {
		typ := binary.BigEndian.Uint16(b)
		l := int(binary.BigEndian.Uint16(b[2:]))
		b = b[4:]
		if l > len(b) {
			return ""
		}
		if typ == 0 { // server_name
			e := b[:l]
			if len(e) < 5 {
				return ""
			}
			e = e[2:] // list length
			if e[0] != 0 {
				return ""
			}
			nl := int(binary.BigEndian.Uint16(e[1:]))
			if 3+nl > len(e) {
				return ""
			}
			return sanitizeName(string(e[3 : 3+nl]))
		}
		b = b[l:]
	}
	return ""
}

// SNIFromTLSRecord parses the first bytes of a TCP payload that starts with a
// TLS record carrying a ClientHello. Returns "" if this is not one.
func SNIFromTLSRecord(p []byte) string {
	if len(p) < 9 || p[0] != 0x16 || p[1] != 0x03 {
		return ""
	}
	rl := int(binary.BigEndian.Uint16(p[3:]))
	body := p[5:]
	if rl < len(body) {
		body = body[:rl]
	}
	return SNIFromClientHello(body)
}

func sanitizeName(s string) string {
	if len(s) == 0 || len(s) > 253 {
		return ""
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_':
			out = append(out, c)
		default:
			return ""
		}
	}
	return string(out)
}
