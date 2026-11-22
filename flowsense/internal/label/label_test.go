package label

import (
	"encoding/hex"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 9001 Appendix A.1
func TestInitialKeysRFC9001(t *testing.T) {
	k := DeriveClientInitial(mustHex(t, "8394c8f03e515708"), 1)
	cases := map[string][]byte{
		"c00cf151ca5be075ed0ebfb5c80323c42d6b7db67881289af4008f1f6c357aea": k.Secret,
		"1f369613dd76d5467730efcbe3b1a22d":                                 k.Key,
		"fa044b2f42a3fd3b46fb255c":                                         k.IV,
		"9f50449e04a0e810283a1e9933adedd2":                                 k.HP,
	}
	for want, got := range cases {
		if hex.EncodeToString(got) != want {
			t.Errorf("got %x want %s", got, want)
		}
	}
}

// RFC 9001 Appendix A.2: decrypt the protected client Initial, recover SNI.
func TestQUICInitialSNI(t *testing.T) {
	h, err := os.ReadFile("../../testdata/rfc9001_a2_client_initial.hex")
	if err != nil {
		t.Fatal(err)
	}
	var a QUICAssembler
	sni, done := a.Add(mustHex(t, strings.TrimSpace(string(h))))
	if !done || sni != "example.com" {
		t.Fatalf("sni=%q done=%v", sni, done)
	}
}

func TestTLSRecordSNI(t *testing.T) {
	// ClientHello from RFC 9001 A.2's CRYPTO frame, wrapped in a TLS record.
	ch := mustHex(t, "010000ed0303ebf8fa56f12939b9584a3896472ec40bb863cfd3e86804fe3a47f06a2b69484c000004130113020100"+
		"00c000000010000e00000b6578616d706c652e636f6dff01000100000a00080006001d0017001800100007000504616c706e"+
		"000500050100000000003300260024001d00209370b2c9caa47fbabaf4559fedba753de171fa71f50f1ce15d43e994ec74d748"+
		"002b0003020304000d0010000e0403050306030203080408050806002d00020101001c00024001003900320408ffffffffffffffff"+
		"05048000ffff07048000ffff0801100104800075300901100f088394c8f03e51570806048000ffff")
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(ch) >> 8), byte(len(ch))}, ch...)
	if s := SNIFromTLSRecord(rec); s != "example.com" {
		t.Fatalf("sni=%q", s)
	}
}

func TestDNSAnswers(t *testing.T) {
	// response: www.youtube.com CNAME youtube-ui.l.google.com, A 142.250.1.2
	m := mustHex(t, "1234818000010002000000000377777707796f7574756265036"+
		"36f6d0000010001c00c000500010000012c00160a796f75747562652d7569016c06676f6f676c65c018"+
		"c02d00010001000000b400048efa0102")
	q, a := DNSAnswers(m)
	if q != "www.youtube.com" || len(a) != 1 || a[0].String() != "142.250.1.2" {
		t.Fatalf("q=%q a=%v", q, a)
	}
	if FromName(q) != "YouTube" || CategoryOf("YouTube") != CatVideo {
		t.Fatal("app mapping")
	}
	if FromName("cdn.xvideos-cdn.com") != "Adult sites" || FromName("example.xxx") != "Adult sites" || FromName("example.org") != AppWeb {
		t.Fatal("adult/web mapping")
	}
}

func TestHTTP(t *testing.T) {
	if h, c := HostFromHTTP([]byte("CONNECT www.reddit.com:443 HTTP/1.1\r\nHost: www.reddit.com:443\r\n\r\n")); h != "www.reddit.com" || !c {
		t.Fatal(h)
	}
	if h, _ := HostFromHTTP([]byte("GET / HTTP/1.1\r\nHost: Example.org\r\n\r\n")); h != "example.org" {
		t.Fatal(h)
	}
}

func TestFromAddress(t *testing.T) {
	if FromAddress(netip.MustParseAddr("128.116.5.9"), true, 55000) != "Roblox" {
		t.Fatal("roblox range")
	}
	if FromAddress(netip.MustParseAddr("20.1.2.3"), true, 3074) != "Xbox Live" {
		t.Fatal("xbox port")
	}
}
