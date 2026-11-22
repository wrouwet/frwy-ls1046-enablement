package ml

import (
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"fmt"
	"io"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"sync"
)

// Sample is one labelled feature vector.
type Sample struct {
	X      []float32
	Label  string
	Source string // "seed" (public dataset) or "local"
}

// Dataset keeps a bounded, class-balanced pool of samples.
type Dataset struct {
	mu       sync.Mutex
	PerClass int
	byClass  map[string][]Sample
	rng      *rand.Rand
	added    int
}

// NewDataset creates a pool that keeps at most perClass samples per label
// (reservoir sampling, local samples preferred over seed samples).
func NewDataset(perClass int) *Dataset {
	return &Dataset{PerClass: perClass, byClass: map[string][]Sample{}, rng: rand.New(rand.NewSource(42))}
}

// Add inserts a sample.
func (d *Dataset) Add(s Sample) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.added++
	l := d.byClass[s.Label]
	if len(l) < d.PerClass {
		d.byClass[s.Label] = append(l, s)
		return
	}
	// replace a random seed sample first; otherwise reservoir-replace
	if s.Source == "local" {
		for tries := 0; tries < 8; tries++ {
			k := d.rng.Intn(len(l))
			if l[k].Source != "local" {
				l[k] = s
				return
			}
		}
	}
	if k := d.rng.Intn(d.added); k < len(l) {
		l[k] = s
	}
}

// Counts returns samples per class and per source.
func (d *Dataset) Counts() (map[string]int, map[string]int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, s := map[string]int{}, map[string]int{}
	for k, l := range d.byClass {
		c[k] = len(l)
		for _, x := range l {
			s[x.Source]++
		}
	}
	return c, s
}

// Snapshot returns a copy of all samples of classes with at least minPerClass.
func (d *Dataset) Snapshot(minPerClass int) []Sample {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Sample
	for _, l := range d.byClass {
		if len(l) >= minPerClass {
			out = append(out, l...)
		}
	}
	return out
}

// Eval is a held-out evaluation of a model.
type Eval struct {
	Accuracy      float64                   `json:"accuracy"`
	N             int                       `json:"n"`
	LocalAccuracy float64                   `json:"local_accuracy"`
	LocalN        int                       `json:"local_n"`
	PerClass      map[string]ClassMetrics   `json:"per_class"`
	Confusion     map[string]map[string]int `json:"confusion"`
}

// ClassMetrics are precision/recall/F1 for one class.
type ClassMetrics struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Support   int     `json:"support"`
}

// TrainEval shuffles samples, trains on 80 % and evaluates on 20 %, then
// (if all=true) retrains on everything for deployment.
func TrainEval(samples []Sample, features []string, p Params, all bool) (*Forest, Eval) {
	rng := rand.New(rand.NewSource(p.Seed))
	rng.Shuffle(len(samples), func(i, j int) { samples[i], samples[j] = samples[j], samples[i] })
	classSet := map[string]bool{}
	for _, s := range samples {
		classSet[s.Label] = true
	}
	var classes []string
	for c := range classSet {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	ci := map[string]int{}
	for i, c := range classes {
		ci[c] = i
	}
	split := len(samples) * 4 / 5
	X, y := toXY(samples[:split], ci)
	f := Train(X, y, classes, features, p)
	ev := Eval{PerClass: map[string]ClassMetrics{}, Confusion: map[string]map[string]int{}}
	tp, fp, fn, sup := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	correct := 0
	for _, s := range samples[split:] {
		pred, _ := f.Best(s.X)
		if ev.Confusion[s.Label] == nil {
			ev.Confusion[s.Label] = map[string]int{}
		}
		ev.Confusion[s.Label][pred]++
		sup[s.Label]++
		if pred == s.Label {
			correct++
			tp[s.Label]++
		} else {
			fp[pred]++
			fn[s.Label]++
		}
	}
	ev.N = len(samples) - split
	if ev.N > 0 {
		ev.Accuracy = float64(correct) / float64(ev.N)
	}
	for _, c := range classes {
		m := ClassMetrics{Support: sup[c]}
		if tp[c]+fp[c] > 0 {
			m.Precision = float64(tp[c]) / float64(tp[c]+fp[c])
		}
		if tp[c]+fn[c] > 0 {
			m.Recall = float64(tp[c]) / float64(tp[c]+fn[c])
		}
		if m.Precision+m.Recall > 0 {
			m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
		}
		ev.PerClass[c] = m
	}
	if all {
		X, y = toXY(samples, ci)
		f = Train(X, y, classes, features, p)
	}
	return f, ev
}

func toXY(s []Sample, ci map[string]int) ([][]float32, []int) {
	X := make([][]float32, len(s))
	y := make([]int, len(s))
	for i := range s {
		X[i], y[i] = s[i].X, ci[s[i].Label]
	}
	return X, y
}

// LoadCSV reads "label,source,f1,f2,..." rows (optionally gzipped) into d.
func LoadCSV(path string, nFeat int, d *Dataset) (int, error) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer fh.Close()
	var r io.Reader = bufio.NewReader(fh)
	if len(path) > 3 && path[len(path)-3:] == ".gz" {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return 0, err
		}
		defer gz.Close()
		r = gz
	}
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	n := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		if rec[0] == "label" && rec[1] == "source" {
			continue // header
		}
		if len(rec) != nFeat+2 {
			return n, fmt.Errorf("row %d: %d fields, want %d", n, len(rec), nFeat+2)
		}
		x := make([]float32, nFeat)
		for j := 0; j < nFeat; j++ {
			v, _ := strconv.ParseFloat(rec[j+2], 32)
			x[j] = float32(v)
		}
		d.Add(Sample{X: x, Label: rec[0], Source: rec[1]})
		n++
	}
	return n, nil
}

// SaveLocalCSV writes local samples (gzip) so learning survives restarts.
func (d *Dataset) SaveLocalCSV(path string) error {
	d.mu.Lock()
	var rows [][]string
	for _, l := range d.byClass {
		for _, s := range l {
			if s.Source != "local" {
				continue
			}
			r := make([]string, 0, len(s.X)+2)
			r = append(r, s.Label, s.Source)
			for _, v := range s.X {
				r = append(r, strconv.FormatFloat(float64(v), 'g', 6, 32))
			}
			rows = append(rows, r)
		}
	}
	d.mu.Unlock()
	tmp := path + ".tmp"
	fh, err := os.Create(tmp)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(fh)
	w := csv.NewWriter(gz)
	if err := w.WriteAll(rows); err != nil {
		fh.Close()
		return err
	}
	gz.Close()
	fh.Close()
	return os.Rename(tmp, path)
}

// Random returns up to n random samples (replay buffer for online learning).
func (d *Dataset) Random(n int, rng *rand.Rand) []Sample {
	d.mu.Lock()
	defer d.mu.Unlock()
	var keys []string
	for k, l := range d.byClass {
		if len(l) > 0 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	out := make([]Sample, 0, n)
	for i := 0; i < n; i++ { // class-balanced replay
		l := d.byClass[keys[rng.Intn(len(keys))]]
		out = append(out, l[rng.Intn(len(l))])
	}
	return out
}

// BothResult is the outcome of training the forest and the network together.
type BothResult struct {
	Forest  *Forest
	Net     *MLP
	EvalRF  Eval
	EvalNet Eval
	EvalEns Eval
	NetW    float64 // ensemble weight of the network, chosen on the hold-out split
	TrainN  int
	LocalN  int
}

// TrainBoth splits samples 80/20, trains a random forest and an MLP on the
// training part (local samples weighted localWeight×), evaluates the forest,
// the network and their average on the test part (overall and local-only),
// then refits both on all samples for deployment.
func TrainBoth(samples []Sample, features []string, p Params, localWeight int) BothResult {
	rng := rand.New(rand.NewSource(p.Seed))
	rng.Shuffle(len(samples), func(i, j int) { samples[i], samples[j] = samples[j], samples[i] })
	classSet := map[string]bool{}
	for _, s := range samples {
		classSet[s.Label] = true
	}
	var classes []string
	for c := range classSet {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	ci := map[string]int{}
	for i, c := range classes {
		ci[c] = i
	}
	weighted := func(s []Sample) ([]Sample, []float64) {
		var out []Sample
		var w []float64
		for _, x := range s {
			k := 1
			if x.Source == "local" {
				k = localWeight
			}
			for i := 0; i < k; i++ {
				out = append(out, x)
			}
			w = append(w, float64(k))
		}
		return out, w
	}
	split := len(samples) * 4 / 5
	train, test := samples[:split], samples[split:]
	tw, _ := weighted(train)
	X, y := toXY(tw, ci)
	rf := Train(X, y, classes, features, p)
	net := NewMLP(train, classes, []int{64, 32}, p.Seed)
	_, w := weighted(train)
	net.Fit(train, w, 12, 64, 2e-3, p.Seed)
	res := BothResult{TrainN: len(samples)}
	for _, s := range samples {
		if s.Source == "local" {
			res.LocalN++
		}
	}
	res.EvalRF = evaluate(test, classes, func(x []float32) []float64 { return rf.Predict(x) })
	res.EvalNet = evaluate(test, classes, func(x []float32) []float64 { return net.Predict(x) })
	// choose the mixing weight on the hold-out split (0 = forest only)
	pf, pn := make([][]float64, len(test)), make([][]float64, len(test))
	for i, s := range test {
		pf[i], pn[i] = rf.Predict(s.X), net.Predict(s.X)
	}
	best := -1.0
	for _, w := range []float64{0, 0.1, 0.2, 0.3, 0.4, 0.5} {
		k := 0
		ev := evaluate(test, classes, func(x []float32) []float64 {
			a := make([]float64, len(pf[k]))
			for i := range a {
				a[i] = (1-w)*pf[k][i] + w*pn[k][i]
			}
			k++
			return a
		})
		if ev.Accuracy > best+0.002 || (w > 0 && ev.Accuracy >= best && res.NetW == 0) {
			best, res.NetW, res.EvalEns = ev.Accuracy, w, ev
		}
	}
	// deploy: refit the forest on everything, give the network one more pass
	all, aw := weighted(samples)
	X, y = toXY(all, ci)
	res.Forest = Train(X, y, classes, features, p)
	_ = aw
	_, w2 := weighted(samples)
	net.Fit(samples, w2, 2, 64, 1e-3, p.Seed+1)
	res.Net = net
	return res
}

func evaluate(test []Sample, classes []string, pred func([]float32) []float64) Eval {
	ev := Eval{PerClass: map[string]ClassMetrics{}, Confusion: map[string]map[string]int{}}
	tp, fp, fn, sup := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	correct, lc, ln := 0, 0, 0
	for _, s := range test {
		p := pred(s.X)
		bi := 0
		for i := range p {
			if p[i] > p[bi] {
				bi = i
			}
		}
		pc := classes[bi]
		if ev.Confusion[s.Label] == nil {
			ev.Confusion[s.Label] = map[string]int{}
		}
		ev.Confusion[s.Label][pc]++
		sup[s.Label]++
		if s.Source == "local" {
			ln++
		}
		if pc == s.Label {
			correct++
			tp[s.Label]++
			if s.Source == "local" {
				lc++
			}
		} else {
			fp[pc]++
			fn[s.Label]++
		}
	}
	ev.N = len(test)
	if ev.N > 0 {
		ev.Accuracy = float64(correct) / float64(ev.N)
	}
	ev.LocalN = ln
	if ln > 0 {
		ev.LocalAccuracy = float64(lc) / float64(ln)
	}
	for _, c := range classes {
		m := ClassMetrics{Support: sup[c]}
		if tp[c]+fp[c] > 0 {
			m.Precision = float64(tp[c]) / float64(tp[c]+fp[c])
		}
		if tp[c]+fn[c] > 0 {
			m.Recall = float64(tp[c]) / float64(tp[c]+fn[c])
		}
		if m.Precision+m.Recall > 0 {
			m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
		}
		ev.PerClass[c] = m
	}
	return ev
}
