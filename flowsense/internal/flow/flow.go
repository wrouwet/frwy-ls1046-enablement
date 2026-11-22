// Package flow keeps per-connection state and turns it into a fixed-length
// feature vector of encrypted-traffic metadata.
//
// The representation follows the CESNET encrypted-traffic datasets
// (CESNET-TLS22 / CESNET-QUIC22, see README): "PPI" = sizes, directions and
// inter-packet times of the first 30 packets that carry payload, plus flow
// statistics. Keeping the same representation lets a model pre-trained on
// those datasets be refined with locally labelled flows.
package flow

import (
	"math"
	"net/netip"
	"sort"
	"time"

	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/capture"
)

// PPILen is the number of payload packets kept per flow.
const PPILen = 30

// Key identifies a flow from the local client's point of view.
type Key struct {
	Proto  uint8
	Client netip.Addr
	CPort  uint16
	Server netip.Addr
	SPort  uint16
}

// Names leaked by the flow itself, used for labelling.
type Names struct {
	SNI     string // TLS or QUIC ClientHello server_name
	Host    string // HTTP Host or proxy CONNECT target
	DNS     string // name the client resolved to the server IP
	Proxied bool   // CONNECT tunnel through the local proxy
}

// Best returns the most trustworthy name and where it came from.
func (n Names) Best() (name, source string) {
	switch {
	case n.SNI != "":
		return n.SNI, "sni"
	case n.Host != "":
		return n.Host, "host"
	case n.DNS != "":
		return n.DNS, "dns"
	}
	return "", ""
}

// Flow is the state of one connection.
type Flow struct {
	Key         Key
	First, Last time.Time

	// whole-flow counters (IP bytes)
	Bytes, BytesRev     int
	Packets, PacketsRev int
	Syn, Fin, Rst       int

	// PPI: first PPILen packets with payload
	ppiSize [PPILen]int16 // signed: + client->server, - server->client
	ppiIPT  [PPILen]float32
	nPPI    int
	ppiLast time.Time

	Names       Names
	NamePending int         // payload packets still worth inspecting for names
	QUIC        interface{} // *label.QUICAssembler while a QUIC SNI is pending
	Closed      bool

	// accounting / classification state
	AcctUp, AcctDown int
	Category         string
	Confidence       float64
	How              string // "name", "model", "rule"
	LabelCat         string // category derived from the name (training label)
	LastClassifiedAt int    // nPPI at last classification
}

// Add updates the flow with a packet; up = client -> server.
func (f *Flow) Add(p *capture.Packet, up bool) {
	if f.First.IsZero() {
		f.First = p.TS
	}
	f.Last = p.TS
	segs := p.Segs
	if segs < 1 {
		segs = 1
	}
	if up {
		f.Bytes += p.Len
		f.Packets += segs
		f.AcctUp += p.Len
	} else {
		f.BytesRev += p.Len
		f.PacketsRev += segs
		f.AcctDown += p.Len
	}
	if p.Proto == 6 {
		if p.Flags&capture.SYN != 0 {
			f.Syn++
		}
		if p.Flags&capture.FIN != 0 {
			f.Fin++
			f.Closed = true
		}
		if p.Flags&capture.RST != 0 {
			f.Rst++
			f.Closed = true
		}
	}
	if p.PayloadLen > 0 && f.nPPI < PPILen {
		per := p.PayloadLen / segs
		for i := 0; i < segs && f.nPPI < PPILen; i++ {
			s := per
			if s > 32767 {
				s = 32767
			}
			if !up {
				s = -s
			}
			f.ppiSize[f.nPPI] = int16(s)
			if f.nPPI > 0 {
				f.ppiIPT[f.nPPI] = float32(p.TS.Sub(f.ppiLast).Seconds() * 1000)
			}
			f.ppiLast = p.TS
			f.nPPI++
		}
	}
}

// PPICount returns how many payload packets are in the PPI.
func (f *Flow) PPICount() int { return f.nPPI }

// FeatureNames lists the model inputs in order (must match Features and the
// CESNET converter in tools/).
var FeatureNames = func() []string {
	var n []string
	for i := 1; i <= PPILen; i++ {
		n = append(n, "ppi_size_"+itoa(i))
	}
	n = append(n, "ppi_len", "ppi_duration_ms", "ppi_roundtrips",
		"ppi_up_cnt", "ppi_down_cnt", "ppi_up_mean", "ppi_up_std", "ppi_up_max", "ppi_down_mean", "ppi_down_std", "ppi_down_max",
		"ppi_ipt_mean", "ppi_ipt_std", "ppi_ipt_max", "ppi_ipt_median")
	for i := 1; i <= 8; i++ {
		n = append(n, "ppi_hist_"+itoa(i))
	}
	n = append(n, "ratio_bytes_rev", "ratio_pkts_rev", "avg_pkt_up", "avg_pkt_down", "is_udp", "port_443")
	return n
}()

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// PPIInput is the raw material for feature computation; it is shared with the
// CESNET converter so both produce identical vectors.
type PPIInput struct {
	Size                []int     // signed payload sizes
	IPT                 []float64 // ms, first = 0
	Bytes, BytesRev     float64
	Packets, PacketsRev float64
	UDP                 bool
	Port443             bool
}

func histBin(s int) int {
	switch {
	case s <= 15:
		return 0
	case s <= 31:
		return 1
	case s <= 63:
		return 2
	case s <= 127:
		return 3
	case s <= 255:
		return 4
	case s <= 511:
		return 5
	case s <= 1024:
		return 6
	}
	return 7
}

// Compute turns a PPIInput into the feature vector.
func Compute(in PPIInput) []float32 {
	v := make([]float32, 0, len(FeatureNames))
	n := len(in.Size)
	if n > PPILen {
		n = PPILen
	}
	for i := 0; i < PPILen; i++ {
		if i < n {
			v = append(v, float32(in.Size[i]))
		} else {
			v = append(v, 0)
		}
	}
	var dur, rt float64
	var up, dn []float64
	var hist [8]float64
	var ipts []float64
	for i := 0; i < n; i++ {
		s := in.Size[i]
		if s >= 0 {
			up = append(up, float64(s))
		} else {
			dn = append(dn, float64(-s))
		}
		a := s
		if a < 0 {
			a = -a
		}
		hist[histBin(a)]++
		if i > 0 {
			dur += in.IPT[i]
			ipts = append(ipts, in.IPT[i])
			if in.Size[i-1] >= 0 && s < 0 { // client->server followed by server->client = one round trip (CESNET PPI_ROUNDTRIPS)
				rt++
			}
		}
	}
	v = append(v, float32(n), float32(dur), float32(rt))
	um, us, ux := stats(up)
	dm, ds, dx := stats(dn)
	v = append(v, float32(len(up)), float32(len(dn)), float32(um), float32(us), float32(ux), float32(dm), float32(ds), float32(dx))
	im, is, ix := stats(ipts)
	v = append(v, float32(im), float32(is), float32(ix), float32(median(ipts)))
	for i := 0; i < 8; i++ {
		if n > 0 {
			v = append(v, float32(hist[i]/float64(n)))
		} else {
			v = append(v, 0)
		}
	}
	udp, p443 := float32(0), float32(0)
	if in.UDP {
		udp = 1
	}
	if in.Port443 {
		p443 = 1
	}
	v = append(v, float32(ratio(in.BytesRev, in.Bytes)), float32(ratio(in.PacketsRev, in.Packets)),
		float32(ratio(in.Bytes, in.Packets)), float32(ratio(in.BytesRev, in.PacketsRev)), udp, p443)
	return v
}

func stats(a []float64) (mean, std, mx float64) {
	if len(a) == 0 {
		return 0, 0, 0
	}
	var s, sq float64
	for _, x := range a {
		s += x
		sq += x * x
		if x > mx {
			mx = x
		}
	}
	mean = s / float64(len(a))
	vr := sq/float64(len(a)) - mean*mean
	if vr < 0 {
		vr = 0
	}
	return mean, math.Sqrt(vr), mx
}

func median(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	c := append([]float64(nil), a...)
	sort.Float64s(c)
	return c[len(c)/2]
}

func ratio(a, b float64) float64 {
	if b <= 0 {
		return a
	}
	return a / b
}

// Features returns the feature vector for the flow so far.
func (f *Flow) Features() []float32 {
	in := PPIInput{
		Size: make([]int, f.nPPI), IPT: make([]float64, f.nPPI),
		Bytes: float64(f.Bytes), BytesRev: float64(f.BytesRev),
		Packets: float64(f.Packets), PacketsRev: float64(f.PacketsRev),
		UDP: f.Key.Proto == 17, Port443: f.Key.SPort == 443,
	}
	for i := 0; i < f.nPPI; i++ {
		in.Size[i] = int(f.ppiSize[i])
		in.IPT[i] = float64(f.ppiIPT[i])
	}
	return Compute(in)
}

// Summary is a few human-readable behaviour facts for the UI.
type Summary struct {
	Packets   int     `json:"packets"`
	Duration  float64 `json:"duration_s"`
	PPS       float64 `json:"pps"`
	UpMean    float64 `json:"up_payload_mean"`
	DownMean  float64 `json:"down_payload_mean"`
	SmallFrac float64 `json:"small_frac"`
	FullFrac  float64 `json:"full_frac"`
	DownUp    float64 `json:"down_up_bytes"`
}

// Summarize computes Summary from the PPI and counters.
func (f *Flow) Summarize() Summary {
	var up, dn []float64
	small, full := 0, 0
	for i := 0; i < f.nPPI; i++ {
		s := int(f.ppiSize[i])
		a := s
		if a < 0 {
			a = -a
			dn = append(dn, float64(a))
		} else {
			up = append(up, float64(a))
		}
		if a <= 200 {
			small++
		}
		if a >= 1200 {
			full++
		}
	}
	um, _, _ := stats(up)
	dm, _, _ := stats(dn)
	dur := f.Last.Sub(f.First).Seconds()
	pk := f.Packets + f.PacketsRev
	r2 := func(x float64) float64 { return math.Round(x*100) / 100 }
	s := Summary{Packets: pk, Duration: math.Round(dur), PPS: r2(float64(pk) / math.Max(dur, 1)),
		UpMean: math.Round(um), DownMean: math.Round(dm), DownUp: r2(ratio(float64(f.BytesRev), float64(f.Bytes)))}
	if f.nPPI > 0 {
		s.SmallFrac, s.FullFrac = r2(float64(small)/float64(f.nPPI)), r2(float64(full)/float64(f.nPPI))
	}
	return s
}
