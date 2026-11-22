package ml

import (
	"math/rand"
	"testing"
)

// Three synthetic "apps" with different packet-size/rate signatures.
func TestForestSynthetic(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var s []Sample
	for i := 0; i < 1500; i++ {
		c := i % 3
		x := make([]float32, 6)
		switch c {
		case 0: // video: large down packets
			x[0], x[1] = float32(1300+rng.Intn(150)), float32(60+rng.Intn(20))
		case 1: // game: small both ways
			x[0], x[1] = float32(80+rng.Intn(60)), float32(70+rng.Intn(60))
		case 2: // web: mixed
			x[0], x[1] = float32(300+rng.Intn(900)), float32(200+rng.Intn(400))
		}
		for j := 2; j < 6; j++ {
			x[j] = rng.Float32()
		}
		s = append(s, Sample{X: x, Label: []string{"video", "game", "web"}[c]})
	}
	p := DefaultParams(6)
	p.Trees = 20
	_, ev := TrainEval(s, []string{"a", "b", "c", "d", "e", "f"}, p, false)
	if ev.Accuracy < 0.95 {
		t.Fatalf("accuracy %.3f", ev.Accuracy)
	}
}
