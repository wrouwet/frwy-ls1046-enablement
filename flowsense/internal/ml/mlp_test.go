package ml

import (
	"math/rand"
	"testing"
)

func synth(n int, seed int64) []Sample {
	rng := rand.New(rand.NewSource(seed))
	var s []Sample
	for i := 0; i < n; i++ {
		c := i % 3
		x := make([]float32, 6)
		switch c {
		case 0:
			x[0], x[1] = float32(1300+rng.Intn(150)), float32(60+rng.Intn(20))
		case 1:
			x[0], x[1] = float32(80+rng.Intn(60)), float32(70+rng.Intn(60))
		case 2:
			x[0], x[1] = float32(300+rng.Intn(900)), float32(200+rng.Intn(400))
		}
		for j := 2; j < 6; j++ {
			x[j] = rng.Float32() * 100
		}
		s = append(s, Sample{X: x, Label: []string{"video", "game", "web"}[c]})
	}
	return s
}

func TestMLPLearnsAndUpdatesOnline(t *testing.T) {
	train := synth(1500, 1)
	classes := []string{"game", "video", "web"}
	n := NewMLP(train, classes, []int{32, 16}, 1)
	n.Fit(train, nil, 15, 32, 3e-3, 1)
	acc := func(m *MLP, s []Sample) float64 {
		ok := 0
		for _, x := range s {
			p := m.Predict(x.X)
			bi := 0
			for i := range p {
				if p[i] > p[bi] {
					bi = i
				}
			}
			if classes[bi] == x.Label {
				ok++
			}
		}
		return float64(ok) / float64(len(s))
	}
	test := synth(600, 2)
	if a := acc(n, test); a < 0.93 {
		t.Fatalf("offline accuracy %.3f", a)
	}
	// online: a new pattern for "game" (larger packets) learned by small steps
	var drift []Sample
	for i := 0; i < 300; i++ {
		drift = append(drift, Sample{X: []float32{float32(700 + i%50), float32(650 + i%40), 1, 2, 3, 4}, Label: "game"})
	}
	before := acc(n, drift)
	c := n.Clone()
	for i := 0; i < 40; i++ {
		b := append(append([]Sample{}, drift[i*5:i*5+5]...), train[i*5:i*5+5]...)
		c.Step(b, nil, 3e-3)
	}
	if after := acc(c, drift); after <= before || after < 0.8 {
		t.Fatalf("online update did not adapt: before %.2f after %.2f", before, after)
	}
	if a := acc(c, test); a < 0.85 {
		t.Fatalf("forgot old classes: %.3f", a)
	}
}
