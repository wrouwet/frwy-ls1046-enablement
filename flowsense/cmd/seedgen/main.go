// seedgen converts raw CESNET PPI exports (tools/export_cesnet.py) into the
// flowsense feature space using the same code the router runs, then trains
// and evaluates the random forest and writes the seed dataset + model.
package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/flow"
	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/ml"
)

func main() {
	in := flag.String("in", "seed_raw.csv.gz", "raw export from tools/export_cesnet.py")
	out := flag.String("out", "seed.csv.gz", "feature CSV for the router")
	modelOut := flag.String("model", "", "also write a trained model JSON here")
	evalOut := flag.String("eval", "", "write evaluation JSON here")
	per := flag.Int("per-class", 6000, "max samples per class")
	trees := flag.Int("trees", 60, "trees")
	flag.Parse()

	fh, err := os.Open(*in)
	if err != nil {
		log.Fatal(err)
	}
	gz, err := gzip.NewReader(fh)
	if err != nil {
		log.Fatal(err)
	}
	r := csv.NewReader(gz)
	hdr, err := r.Read()
	if err != nil {
		log.Fatal(err)
	}
	col := map[string]int{}
	for i, h := range hdr {
		col[h] = i
	}
	ds := ml.NewDataset(*per)
	n := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		f := func(k string) float64 { v, _ := strconv.ParseFloat(rec[col[k]], 64); return v }
		pin := flow.PPIInput{UDP: rec[col["udp"]] == "1", Port443: true}
		for i := 1; i <= flow.PPILen; i++ {
			s := int(f("s" + strconv.Itoa(i)))
			if s == 0 {
				break
			}
			pin.Size = append(pin.Size, s)
			pin.IPT = append(pin.IPT, f("t"+strconv.Itoa(i)))
		}
		if len(pin.Size) < 2 {
			continue
		}
		// one sample per classification point the router uses (8, 15, 30 packets)
		for _, k := range flow.Horizons {
			if k > len(pin.Size) && k != flow.Horizons[0] {
				break
			}
			ds.Add(ml.Sample{X: flow.Compute(pin.Prefix(k)), Label: rec[col["app"]], Source: "seed"})
			if k >= len(pin.Size) {
				break
			}
		}
		n++
	}
	samples := ds.Snapshot(1)
	log.Printf("read %d flows, kept %d samples, %d features", n, len(samples), len(flow.FeatureNames))

	// write seed CSV
	of, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	ogz := gzip.NewWriter(of)
	w := csv.NewWriter(ogz)
	w.Write(append([]string{"label", "source"}, flow.FeatureNames...))
	for _, s := range samples {
		row := []string{s.Label, "seed"}
		for _, v := range s.X {
			row = append(row, strconv.FormatFloat(float64(v), 'g', 6, 32))
		}
		w.Write(row)
	}
	w.Flush()
	ogz.Close()
	of.Close()

	p := ml.DefaultParams(len(flow.FeatureNames))
	p.Trees = *trees
	t0 := time.Now()
	res := ml.TrainBoth(samples, flow.FeatureNames, p, 3)
	model, ev := res.Forest, res.EvalRF
	log.Printf("trained in %s: hold-out accuracy forest %.3f, network %.3f, ensemble %.3f (network weight %.1f) on %d samples",
		time.Since(t0).Round(time.Second), res.EvalRF.Accuracy, res.EvalNet.Accuracy, res.EvalEns.Accuracy, res.NetW, ev.N)
	var names []string
	for c := range ev.PerClass {
		names = append(names, c)
	}
	sort.Slice(names, func(i, j int) bool { return res.EvalEns.PerClass[names[i]].F1 > res.EvalEns.PerClass[names[j]].F1 })
	fmt.Printf("%-26s %7s %7s %7s %6s\n", "class", "F1 rf", "F1 net", "F1 ens", "n")
	for _, c := range names {
		fmt.Printf("%-26s %7.2f %7.2f %7.2f %6d\n", c, res.EvalRF.PerClass[c].F1, res.EvalNet.PerClass[c].F1, res.EvalEns.PerClass[c].F1, ev.PerClass[c].Support)
	}
	type fi struct {
		Name string
		V    float64
	}
	var imp []fi
	for i, v := range model.Importance {
		imp = append(imp, fi{flow.FeatureNames[i], v})
	}
	sort.Slice(imp, func(i, j int) bool { return imp[i].V > imp[j].V })
	fmt.Print("top features:")
	for _, x := range imp[:12] {
		fmt.Printf(" %s=%.3f", x.Name, x.V)
	}
	fmt.Println()
	if *modelOut != "" {
		if err := model.Save(*modelOut); err != nil {
			log.Fatal(err)
		}
	}
	if *evalOut != "" {
		b, _ := json.MarshalIndent(ev, "", " ")
		os.WriteFile(*evalOut, b, 0644)
	}
}
