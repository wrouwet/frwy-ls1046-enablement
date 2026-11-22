// Package ml is a small, dependency-free random forest (CART, Gini impurity,
// bootstrap aggregation, random feature subspaces) that trains and predicts
// on the router itself.
package ml

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"sync"
)

// Node is one tree node. Leaves have Feat == -1 and a class distribution.
type Node struct {
	Feat  int       `json:"f"`
	Thr   float32   `json:"t,omitempty"`
	Left  int32     `json:"l,omitempty"`
	Right int32     `json:"r,omitempty"`
	Dist  []float32 `json:"d,omitempty"`
}

// Tree is a flat array of nodes; index 0 is the root.
type Tree struct {
	Nodes []Node `json:"n"`
}

// Forest is the trained model.
type Forest struct {
	Classes    []string  `json:"classes"`
	Features   []string  `json:"features"`
	Trees      []Tree    `json:"trees"`
	Importance []float64 `json:"importance"`
}

// Params control training.
type Params struct {
	Trees, MaxDepth, MinLeaf, MaxFeatures, Bins int
	Seed                                        int64
}

// DefaultParams are sized for an ARM Cortex-A72 and ~50k samples.
func DefaultParams(nFeat int) Params {
	return Params{Trees: 60, MaxDepth: 16, MinLeaf: 3, MaxFeatures: int(math.Sqrt(float64(nFeat))) + 1, Bins: 32, Seed: 1}
}

// Train fits a forest on X (rows) with integer labels y in [0, len(classes)).
func Train(X [][]float32, y []int, classes, features []string, p Params) *Forest {
	nF := len(features)
	f := &Forest{Classes: classes, Features: features, Trees: make([]Tree, p.Trees), Importance: make([]float64, nF)}
	// candidate thresholds per feature: quantiles (keeps training O(n·bins))
	thr := make([][]float32, nF)
	col := make([]float32, len(X))
	for j := 0; j < nF; j++ {
		for i := range X {
			col[i] = X[i][j]
		}
		thr[j] = quantiles(col, p.Bins)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for t := 0; t < p.Trees; t++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(t int) {
			defer wg.Done()
			defer func() { <-sem }()
			rng := rand.New(rand.NewSource(p.Seed + int64(t)*7919))
			idx := make([]int, len(X))
			for i := range idx {
				idx[i] = rng.Intn(len(X))
			}
			b := &builder{X: X, y: y, nC: len(classes), thr: thr, p: p, rng: rng, imp: make([]float64, nF)}
			b.grow(idx, 0)
			mu.Lock()
			f.Trees[t] = Tree{Nodes: b.nodes}
			for j := range b.imp {
				f.Importance[j] += b.imp[j]
			}
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	var s float64
	for _, v := range f.Importance {
		s += v
	}
	if s > 0 {
		for j := range f.Importance {
			f.Importance[j] /= s
		}
	}
	return f
}

func quantiles(col []float32, bins int) []float32 {
	c := append([]float32(nil), col...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	var out []float32
	for b := 1; b < bins; b++ {
		v := c[len(c)*b/bins]
		if len(out) == 0 || v > out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

type builder struct {
	X     [][]float32
	y     []int
	nC    int
	thr   [][]float32
	p     Params
	rng   *rand.Rand
	nodes []Node
	imp   []float64
}

func gini(cnt []float64, n float64) float64 {
	if n == 0 {
		return 0
	}
	g := 1.0
	for _, c := range cnt {
		q := c / n
		g -= q * q
	}
	return g
}

func (b *builder) leaf(idx []int) int32 {
	d := make([]float32, b.nC)
	for _, i := range idx {
		d[b.y[i]]++
	}
	for k := range d {
		d[k] /= float32(len(idx))
	}
	b.nodes = append(b.nodes, Node{Feat: -1, Dist: d})
	return int32(len(b.nodes) - 1)
}

func (b *builder) grow(idx []int, depth int) int32 {
	cnt := make([]float64, b.nC)
	for _, i := range idx {
		cnt[b.y[i]]++
	}
	n := float64(len(idx))
	parent := gini(cnt, n)
	if depth >= b.p.MaxDepth || len(idx) < 2*b.p.MinLeaf || parent == 0 {
		return b.leaf(idx)
	}
	nF := len(b.thr)
	feats := b.rng.Perm(nF)[:min(b.p.MaxFeatures, nF)]
	bestGain, bestF, bestT := 0.0, -1, float32(0)
	for _, j := range feats {
		th := b.thr[j]
		if len(th) == 0 {
			continue
		}
		// histogram of classes per threshold bucket
		hist := make([][]float64, len(th)+1)
		for k := range hist {
			hist[k] = make([]float64, b.nC)
		}
		for _, i := range idx {
			v := b.X[i][j]
			k := sort.Search(len(th), func(q int) bool { return v <= th[q] })
			hist[k][b.y[i]]++
		}
		left := make([]float64, b.nC)
		var nl float64
		for k := 0; k < len(th); k++ {
			for c := 0; c < b.nC; c++ {
				left[c] += hist[k][c]
				nl += hist[k][c]
			}
			nr := n - nl
			if nl < float64(b.p.MinLeaf) || nr < float64(b.p.MinLeaf) {
				continue
			}
			right := make([]float64, b.nC)
			for c := range right {
				right[c] = cnt[c] - left[c]
			}
			g := parent - (nl/n)*gini(left, nl) - (nr/n)*gini(right, nr)
			if g > bestGain {
				bestGain, bestF, bestT = g, j, th[k]
			}
		}
	}
	if bestF < 0 {
		return b.leaf(idx)
	}
	b.imp[bestF] += bestGain * n
	var li, ri []int
	for _, i := range idx {
		if b.X[i][bestF] <= bestT {
			li = append(li, i)
		} else {
			ri = append(ri, i)
		}
	}
	b.nodes = append(b.nodes, Node{Feat: bestF, Thr: bestT})
	me := int32(len(b.nodes) - 1)
	l := b.grow(li, depth+1)
	r := b.grow(ri, depth+1)
	b.nodes[me].Left, b.nodes[me].Right = l, r
	return me
}

// Predict returns class probabilities (averaged leaf distributions).
func (f *Forest) Predict(x []float32) []float64 {
	out := make([]float64, len(f.Classes))
	for _, t := range f.Trees {
		n := 0
		for t.Nodes[n].Feat >= 0 {
			nd := t.Nodes[n]
			if x[nd.Feat] <= nd.Thr {
				n = int(nd.Left)
			} else {
				n = int(nd.Right)
			}
		}
		for k, v := range t.Nodes[n].Dist {
			out[k] += float64(v)
		}
	}
	for k := range out {
		out[k] /= float64(len(f.Trees))
	}
	return out
}

// Best returns the top class and its probability.
func (f *Forest) Best(x []float32) (string, float64) {
	p := f.Predict(x)
	bi := 0
	for k := range p {
		if p[k] > p[bi] {
			bi = k
		}
	}
	return f.Classes[bi], p[bi]
}

// Save writes the forest as JSON.
func (f *Forest) Save(path string) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a forest written by Save.
func Load(path string) (*Forest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Forest
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}
