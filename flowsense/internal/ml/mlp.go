package ml

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
)

// MLP is a small fully-connected network (inputs -> 64 -> 32 -> classes,
// ReLU, softmax) trained with Adam. Unlike the forest it can be updated
// continuously: every new labelled flow is a gradient step (mixed with
// replayed older samples so it does not forget).
type MLP struct {
	Classes  []string    `json:"classes"`
	Mean     []float64   `json:"mean"` // input normalisation (after signed log1p)
	Std      []float64   `json:"std"`
	W        [][]float64 `json:"w"` // per layer, row-major [out][in]
	B        [][]float64 `json:"b"`
	Sizes    []int       `json:"sizes"`
	Steps    int         `json:"steps"`
	m, v     [][]float64 // Adam moments for W
	mb, vb   [][]float64 // Adam moments for B
	classIdx map[string]int
}

func slog(x float32) float64 {
	if x < 0 {
		return -math.Log1p(float64(-x))
	}
	return math.Log1p(float64(x))
}

// NewMLP initialises a network for the given classes, with normalisation
// statistics taken from the samples.
func NewMLP(samples []Sample, classes []string, hidden []int, seed int64) *MLP {
	nIn := len(samples[0].X)
	n := &MLP{Classes: classes, Mean: make([]float64, nIn), Std: make([]float64, nIn)}
	for _, s := range samples {
		for j, x := range s.X {
			n.Mean[j] += slog(x)
		}
	}
	for j := range n.Mean {
		n.Mean[j] /= float64(len(samples))
	}
	for _, s := range samples {
		for j, x := range s.X {
			d := slog(x) - n.Mean[j]
			n.Std[j] += d * d
		}
	}
	for j := range n.Std {
		n.Std[j] = math.Sqrt(n.Std[j]/float64(len(samples))) + 1e-6
	}
	n.Sizes = append(append([]int{nIn}, hidden...), len(classes))
	rng := rand.New(rand.NewSource(seed))
	for l := 0; l+1 < len(n.Sizes); l++ {
		in, out := n.Sizes[l], n.Sizes[l+1]
		w := make([]float64, in*out)
		sc := math.Sqrt(2.0 / float64(in)) // He init
		for i := range w {
			w[i] = rng.NormFloat64() * sc
		}
		n.W = append(n.W, w)
		n.B = append(n.B, make([]float64, out))
	}
	n.init()
	return n
}

func (n *MLP) init() {
	n.classIdx = map[string]int{}
	for i, c := range n.Classes {
		n.classIdx[c] = i
	}
	n.m, n.v, n.mb, n.vb = nil, nil, nil, nil
	for l := range n.W {
		n.m = append(n.m, make([]float64, len(n.W[l])))
		n.v = append(n.v, make([]float64, len(n.W[l])))
		n.mb = append(n.mb, make([]float64, len(n.B[l])))
		n.vb = append(n.vb, make([]float64, len(n.B[l])))
	}
}

func (n *MLP) norm(x []float32) []float64 {
	z := make([]float64, len(x))
	for j, v := range x {
		z[j] = (slog(v) - n.Mean[j]) / n.Std[j]
		if z[j] > 8 {
			z[j] = 8
		} else if z[j] < -8 {
			z[j] = -8
		}
	}
	return z
}

// forward returns activations per layer (acts[0] = normalised input) and softmax output.
func (n *MLP) forward(x []float32) ([][]float64, []float64) {
	acts := [][]float64{n.norm(x)}
	for l := range n.W {
		in, out := n.Sizes[l], n.Sizes[l+1]
		a := acts[l]
		z := make([]float64, out)
		for o := 0; o < out; o++ {
			s := n.B[l][o]
			row := n.W[l][o*in : (o+1)*in]
			for i, v := range a {
				s += row[i] * v
			}
			if l+1 < len(n.W) && s < 0 {
				s = 0 // ReLU on hidden layers
			}
			z[o] = s
		}
		acts = append(acts, z)
	}
	logits := acts[len(acts)-1]
	mx := logits[0]
	for _, v := range logits {
		if v > mx {
			mx = v
		}
	}
	p := make([]float64, len(logits))
	var sum float64
	for i, v := range logits {
		p[i] = math.Exp(v - mx)
		sum += p[i]
	}
	for i := range p {
		p[i] /= sum
	}
	return acts, p
}

// Predict returns class probabilities.
func (n *MLP) Predict(x []float32) []float64 { _, p := n.forward(x); return p }

// Step does one Adam update on a mini-batch; samples with unknown labels are
// skipped. weights scale each sample's gradient (nil = 1). Returns mean loss.
func (n *MLP) Step(batch []Sample, weights []float64, lr float64) float64 {
	if n.m == nil {
		n.init()
	}
	gW := make([][]float64, len(n.W))
	gB := make([][]float64, len(n.B))
	for l := range n.W {
		gW[l] = make([]float64, len(n.W[l]))
		gB[l] = make([]float64, len(n.B[l]))
	}
	var loss, wsum float64
	for bi, s := range batch {
		y, ok := n.classIdx[s.Label]
		if !ok {
			continue
		}
		w := 1.0
		if weights != nil {
			w = weights[bi]
		}
		acts, p := n.forward(s.X)
		loss += -math.Log(math.Max(p[y], 1e-12)) * w
		wsum += w
		delta := make([]float64, len(p))
		for i := range p {
			delta[i] = p[i] * w
		}
		delta[y] -= w
		for l := len(n.W) - 1; l >= 0; l-- {
			in := n.Sizes[l]
			a := acts[l]
			for o, d := range delta {
				if d == 0 {
					continue
				}
				gB[l][o] += d
				row := gW[l][o*in : (o+1)*in]
				for i, v := range a {
					row[i] += d * v
				}
			}
			if l == 0 {
				break
			}
			prev := make([]float64, in)
			for o, d := range delta {
				if d == 0 {
					continue
				}
				row := n.W[l][o*in : (o+1)*in]
				for i := range prev {
					prev[i] += d * row[i]
				}
			}
			for i := range prev { // ReLU derivative
				if acts[l][i] <= 0 {
					prev[i] = 0
				}
			}
			delta = prev
		}
	}
	if wsum == 0 {
		return 0
	}
	n.Steps++
	const b1, b2, eps = 0.9, 0.999, 1e-8
	c1 := 1 - math.Pow(b1, float64(n.Steps))
	c2 := 1 - math.Pow(b2, float64(n.Steps))
	upd := func(p, g, m, v []float64) {
		for i := range p {
			gi := g[i] / wsum
			m[i] = b1*m[i] + (1-b1)*gi
			v[i] = b2*v[i] + (1-b2)*gi*gi
			p[i] -= lr * (m[i] / c1) / (math.Sqrt(v[i]/c2) + eps)
		}
	}
	for l := range n.W {
		upd(n.W[l], gW[l], n.m[l], n.v[l])
		upd(n.B[l], gB[l], n.mb[l], n.vb[l])
	}
	return loss / wsum
}

// Fit trains for a number of epochs with shuffled mini-batches.
func (n *MLP) Fit(samples []Sample, weights []float64, epochs, batch int, lr float64, seed int64) float64 {
	rng := rand.New(rand.NewSource(seed))
	idx := rng.Perm(len(samples))
	var last float64
	for e := 0; e < epochs; e++ {
		rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
		var tot float64
		var nb int
		for s := 0; s < len(idx); s += batch {
			end := s + batch
			if end > len(idx) {
				end = len(idx)
			}
			b := make([]Sample, 0, end-s)
			var w []float64
			for _, k := range idx[s:end] {
				b = append(b, samples[k])
				if weights != nil {
					w = append(w, weights[k])
				}
			}
			tot += n.Step(b, w, lr)
			nb++
		}
		last = tot / float64(nb)
	}
	return last
}

// Clone returns a deep copy (for copy-on-write online updates).
func (n *MLP) Clone() *MLP {
	c := *n
	c.W, c.B = make([][]float64, len(n.W)), make([][]float64, len(n.B))
	c.m, c.v, c.mb, c.vb = make([][]float64, len(n.W)), make([][]float64, len(n.W)), make([][]float64, len(n.W)), make([][]float64, len(n.W))
	for l := range n.W {
		c.W[l] = append([]float64(nil), n.W[l]...)
		c.B[l] = append([]float64(nil), n.B[l]...)
		if n.m != nil {
			c.m[l] = append([]float64(nil), n.m[l]...)
			c.v[l] = append([]float64(nil), n.v[l]...)
			c.mb[l] = append([]float64(nil), n.mb[l]...)
			c.vb[l] = append([]float64(nil), n.vb[l]...)
		}
	}
	if n.m == nil {
		c.m, c.v, c.mb, c.vb = nil, nil, nil, nil
	}
	c.classIdx = n.classIdx
	return &c
}

// Save writes the network as JSON.
func (n *MLP) Save(path string) error {
	b, err := json.Marshal(n)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", b, 0644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// LoadMLP reads a network written by Save.
func LoadMLP(path string) (*MLP, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n MLP
	if err := json.Unmarshal(b, &n); err != nil {
		return nil, err
	}
	n.init()
	return &n, nil
}
