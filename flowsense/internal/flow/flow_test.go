package flow

import (
	"net/netip"
	"testing"
	"time"

	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/capture"
)

// Live features after k payload packets must equal the training features of
// the same flow truncated to k packets (prefix consistency).
func TestPrefixConsistency(t *testing.T) {
	f := &Flow{Key: Key{Proto: 6, SPort: 443, Client: netip.MustParseAddr("192.168.50.2"), Server: netip.MustParseAddr("1.1.1.1")}}
	t0 := time.Unix(1000, 0)
	sizes := []int{517, -1400, -1400, -900, 80, 64, -1300, -1300, -1300, 90, -600, 70, 64, -1400, -1400, 33}
	var in PPIInput
	in.Port443 = true
	for i, s := range sizes {
		up := s > 0
		a := s
		if a < 0 {
			a = -a
		}
		ts := t0.Add(time.Duration(i*7) * time.Millisecond)
		f.Add(&capture.Packet{TS: ts, Len: a + 52, Proto: 6, PayloadLen: a, Segs: 1, Flags: capture.ACK}, up)
		in.Size = append(in.Size, s)
		ipt := 0.0
		if i > 0 {
			ipt = 7
		}
		in.IPT = append(in.IPT, ipt)
		if i+1 == 8 || i+1 == 15 {
			a, b := f.Features(), Compute(in.Prefix(i+1))
			if len(a) != len(FeatureNames) {
				t.Fatalf("len %d vs %d names", len(a), len(FeatureNames))
			}
			for j := range a {
				if d := a[j] - b[j]; d > 1e-3 || d < -1e-3 {
					t.Fatalf("k=%d feature %s: live %v train %v", i+1, FeatureNames[j], a[j], b[j])
				}
			}
		}
	}
}
