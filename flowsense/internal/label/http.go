package label

import "bytes"

// HostFromHTTP returns the target of a proxy "CONNECT host:port" request or
// the Host header of a plain HTTP request found at the start of a TCP payload.
func HostFromHTTP(p []byte) (host string, isConnect bool) {
	if len(p) > 2048 {
		p = p[:2048]
	}
	if bytes.HasPrefix(p, []byte("CONNECT ")) {
		rest := p[8:]
		if i := bytes.IndexAny(rest, ": "); i > 0 {
			return sanitizeName(string(rest[:i])), true
		}
		return "", true
	}
	methods := [][]byte{[]byte("GET "), []byte("POST "), []byte("HEAD "), []byte("PUT "), []byte("OPTIONS ")}
	ok := false
	for _, m := range methods {
		if bytes.HasPrefix(p, m) {
			ok = true
			break
		}
	}
	if !ok {
		return "", false
	}
	// absolute-form request line (proxy): GET http://host/...
	if i := bytes.Index(p, []byte(" http://")); i > 0 && i < 16 {
		r := p[i+8:]
		if j := bytes.IndexAny(r, ":/ "); j > 0 {
			return sanitizeName(string(r[:j])), false
		}
	}
	for _, line := range bytes.Split(p, []byte("\r\n")) {
		if len(line) > 5 && bytes.EqualFold(line[:5], []byte("host:")) {
			h := bytes.TrimSpace(line[5:])
			if i := bytes.IndexByte(h, ':'); i > 0 {
				h = h[:i]
			}
			return sanitizeName(string(h)), false
		}
	}
	return "", false
}
