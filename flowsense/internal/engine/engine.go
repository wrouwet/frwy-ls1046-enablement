// Package engine ties capture, flow tracking, labelling, the model and the
// usage statistics together. All packet processing happens on one goroutine;
// the API reads snapshots that are published every few seconds.
package engine

import (
	"bufio"
	"encoding/json"
	"log"
	"math"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/capture"
	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/flow"
	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/label"
	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/ml"
)

// Config is the engine configuration.
type Config struct {
	LocalNets  []netip.Prefix // client networks (LAN side)
	RouterIPs  []netip.Addr   // this router's own addresses on the LAN
	ProxyPort  uint16         // local HTTP proxy port (CONNECT tunnels), 0 = none
	DataDir    string         // persistent state
	SeedCSV    string         // seed dataset (public data), optional
	MinConf    float64        // minimum model confidence to show a prediction
	Leases     string         // dnsmasq leases file for client names
	NamesFile  string         // optional JSON {mac: {name: ...}} with user-given names
	RetrainMin time.Duration  // retrain interval
}

type dnsEnt struct {
	name string
	exp  time.Time
}

type acct struct{ Down, Up float64 }

// Engine is the classifier state.
type Engine struct {
	cfg    Config
	flows  map[flow.Key]*flow.Flow
	dns    map[netip.Addr]dnsEnt
	clhbuf map[flow.Key][]byte // client payload reassembly for TLS ClientHello

	ds        *ml.Dataset
	model     atomic.Pointer[ml.Forest]
	net       atomic.Pointer[ml.MLP]
	training  atomic.Bool
	evalMu    sync.Mutex
	eval      ml.Eval // forest
	evalNet   ml.Eval
	evalEns   ml.Eval
	trainedAt time.Time
	newLocal  int
	history   []Round

	queueMu   sync.Mutex
	queue     []ml.Sample // new local samples waiting for an online network update
	online    atomic.Int64
	wRF, wNet float64 // ensemble weights (from live agreement)
	live      map[string]*liveScore

	// live statistics (engine goroutine only)
	tickRate map[string]*acct            // app -> bytes in last tick
	tickCli  map[string]map[string]*acct // client -> app -> bytes in last tick
	today    map[string]*acct            // app -> bytes today
	todayCli map[string]map[string]float64
	day      string
	hist     []histBucket
	agree    map[string][2]int // app -> [model agreed, labelled flows]
	pkts     uint64
	lastTick time.Time

	hostsMu sync.Mutex
	hosts   map[netip.Addr]hostInfo

	snapMu sync.RWMutex
	snap   Snapshot
}

type hostInfo struct{ MAC, Name string }

// liveScore tracks how often a model agrees with names seen on this network
// (exponentially weighted so it follows the model as it learns).
type liveScore struct {
	Agree, Total int
	EWMA         float64
}

func (l *liveScore) add(ok bool) {
	l.Total++
	x := 0.0
	if ok {
		l.Agree++
		x = 1
	}
	if l.Total == 1 {
		l.EWMA = x
	} else {
		l.EWMA = 0.98*l.EWMA + 0.02*x
	}
}

// Round is one point of the learning curve.
type Round struct {
	T          int64   `json:"t"`
	Kind       string  `json:"kind"` // "retrain" or "online"
	Samples    int     `json:"samples"`
	Local      int     `json:"local"`
	Classes    int     `json:"classes"`
	AccRF      float64 `json:"acc_rf,omitempty"`
	AccNet     float64 `json:"acc_net,omitempty"`
	AccEns     float64 `json:"acc_ens,omitempty"`
	LocalAccRF float64 `json:"local_acc_rf,omitempty"`
	LocalAccNt float64 `json:"local_acc_net,omitempty"`
	LocalAccEn float64 `json:"local_acc_ens,omitempty"`
	LiveRF     float64 `json:"live_rf"`
	LiveNet    float64 `json:"live_net"`
	LiveEns    float64 `json:"live_ens"`
	Seconds    float64 `json:"seconds,omitempty"`
}

type histBucket struct {
	T    int64              `json:"t"`
	Apps map[string]float64 `json:"apps"`
}

// New creates an engine and loads persisted state, seed data and model.
func New(cfg Config) *Engine {
	e := &Engine{cfg: cfg, flows: map[flow.Key]*flow.Flow{}, dns: map[netip.Addr]dnsEnt{}, clhbuf: map[flow.Key][]byte{},
		ds: ml.NewDataset(4000), tickRate: map[string]*acct{}, tickCli: map[string]map[string]*acct{},
		today: map[string]*acct{}, todayCli: map[string]map[string]float64{}, agree: map[string][2]int{}, hosts: map[netip.Addr]hostInfo{},
		wRF: 0.8, wNet: 0.2, live: map[string]*liveScore{"rf": {}, "net": {}, "ens": {}}}
	os.MkdirAll(cfg.DataDir, 0755)
	if cfg.SeedCSV != "" {
		if n, err := ml.LoadCSV(cfg.SeedCSV, len(flow.FeatureNames), e.ds); err != nil {
			log.Printf("seed: %v", err)
		} else {
			log.Printf("seed: %d samples from %s", n, cfg.SeedCSV)
		}
	}
	if n, err := ml.LoadCSV(filepath.Join(cfg.DataDir, "local.csv.gz"), len(flow.FeatureNames), e.ds); err == nil {
		log.Printf("local: %d samples restored", n)
	}
	if m, err := ml.Load(filepath.Join(cfg.DataDir, "model.json")); err == nil && len(m.Features) == len(flow.FeatureNames) {
		e.model.Store(m)
		if b, err := os.ReadFile(filepath.Join(cfg.DataDir, "eval.json")); err == nil {
			json.Unmarshal(b, &e.eval)
		}
		if st, err := os.Stat(filepath.Join(cfg.DataDir, "model.json")); err == nil {
			e.trainedAt = st.ModTime()
		}
		log.Printf("model: loaded (%d classes)", len(m.Classes))
	}
	if n, err := ml.LoadMLP(filepath.Join(cfg.DataDir, "net.json")); err == nil && len(n.Mean) == len(flow.FeatureNames) {
		e.net.Store(n)
		log.Printf("network: loaded (%d steps)", n.Steps)
	}
	if b, err := os.ReadFile(filepath.Join(cfg.DataDir, "history.json")); err == nil {
		json.Unmarshal(b, &e.history)
	}
	e.loadState()
	e.refreshHosts()
	return e
}

func (e *Engine) isLocal(a netip.Addr) bool {
	for _, p := range e.cfg.LocalNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (e *Engine) isRouter(a netip.Addr) bool {
	for _, r := range e.cfg.RouterIPs {
		if r == a {
			return true
		}
	}
	return false
}

// Handle processes one packet (engine goroutine).
func (e *Engine) Handle(p *capture.Packet) {
	e.pkts++
	srcL, dstL := e.isLocal(p.Src), e.isLocal(p.Dst)
	// DNS answers from the router to clients: remember name -> address
	if p.Proto == 17 && p.SPort == 53 && dstL && len(p.Payload) > 0 {
		if q, addrs := label.DNSAnswers(p.Payload); q != "" {
			for _, a := range addrs {
				e.dns[a] = dnsEnt{q, p.TS.Add(2 * time.Hour)}
			}
		}
		return
	}
	var k flow.Key
	var up bool
	switch {
	case srcL && !dstL:
		k, up = flow.Key{Proto: p.Proto, Client: p.Src, CPort: p.SPort, Server: p.Dst, SPort: p.DPort}, true
	case dstL && !srcL:
		k, up = flow.Key{Proto: p.Proto, Client: p.Dst, CPort: p.DPort, Server: p.Src, SPort: p.SPort}, false
	case srcL && dstL && e.cfg.ProxyPort != 0 && p.Proto == 6 && e.isRouter(p.Dst) && p.DPort == e.cfg.ProxyPort:
		k, up = flow.Key{Proto: 6, Client: p.Src, CPort: p.SPort, Server: p.Dst, SPort: p.DPort}, true
	case srcL && dstL && e.cfg.ProxyPort != 0 && p.Proto == 6 && e.isRouter(p.Src) && p.SPort == e.cfg.ProxyPort:
		k, up = flow.Key{Proto: 6, Client: p.Dst, CPort: p.DPort, Server: p.Src, SPort: p.SPort}, false
	default:
		return // LAN-internal or transit we don't care about
	}
	f := e.flows[k]
	if f == nil {
		f = &flow.Flow{Key: k, NamePending: 4}
		if d, ok := e.dns[k.Server]; ok {
			f.Names.DNS = d.name
		}
		f.Names.Proxied = e.isRouter(k.Server)
		e.flows[k] = f
	}
	f.Add(p, up)
	if up && p.PayloadLen > 0 && f.NamePending > 0 {
		e.inspect(f, p)
	}
	n := f.PPICount()
	if f.Category == "" && p.TS.Sub(f.First) > 5*time.Second {
		e.classify(f)
		return
	}
	for _, h := range flow.Horizons {
		if n >= h && f.LastClassifiedAt < h {
			e.classify(f)
			break
		}
	}
}

// inspect looks for names in the first client payload packets.
func (e *Engine) inspect(f *flow.Flow, p *capture.Packet) {
	f.NamePending--
	k := f.Key
	if k.Proto == 17 {
		if k.SPort == 443 {
			qa, _ := f.QUIC.(*label.QUICAssembler)
			if qa == nil {
				qa = &label.QUICAssembler{}
				f.QUIC = qa
			}
			if sni, done := qa.Add(p.Payload); done {
				f.Names.SNI = sni
				f.QUIC = nil
				f.NamePending = 0
			}
		}
		return
	}
	if h, isConnect := label.HostFromHTTP(p.Payload); h != "" || isConnect {
		f.Names.Host = h
		if !isConnect {
			f.NamePending = 0
		}
		return
	}
	buf := append(e.clhbuf[k], p.Payload...)
	if len(buf) > 8192 {
		buf = buf[:8192]
	}
	if len(buf) >= 5 && buf[0] == 0x16 {
		if sni := label.SNIFromTLSRecord(buf); sni != "" {
			f.Names.SNI = sni
			f.NamePending = 0
			delete(e.clhbuf, k)
			return
		}
		e.clhbuf[k] = buf
	}
	if f.NamePending == 0 {
		delete(e.clhbuf, k)
	}
}

// realtime heuristics for UDP that has no name: games and voice.
func udpBehaviour(f *flow.Flow) string {
	s := f.Summarize()
	if f.Key.Proto != 17 || f.Key.SPort == 443 || f.Key.SPort == 53 || s.Packets < 20 {
		return ""
	}
	if s.SmallFrac >= 0.8 && s.PPS >= 15 && s.DownUp > 0.2 && s.DownUp < 6 {
		if s.UpMean >= 60 && s.UpMean <= 220 && s.PPS >= 40 && s.PPS <= 140 && s.DownUp > 0.6 && s.DownUp < 1.7 {
			return "Voice chat (unknown app)"
		}
		return "Online game (unknown)"
	}
	return ""
}

// predict runs the forest and the network on a feature vector and returns
// each model's top class plus the weighted ensemble's top class/confidence.
func (e *Engine) predict(x []float32) (rf, net, ens string, conf float64) {
	m, n := e.model.Load(), e.net.Load()
	if m == nil {
		return
	}
	pr := m.Predict(x)
	best := func(p []float64, cls []string) (string, float64) {
		bi := 0
		for i := range p {
			if p[i] > p[bi] {
				bi = i
			}
		}
		return cls[bi], p[bi]
	}
	rf, _ = best(pr, m.Classes)
	if n == nil {
		ens, conf = best(pr, m.Classes)
		return
	}
	pn := n.Predict(x)
	net, _ = best(pn, n.Classes)
	idx := map[string]int{}
	for i, c := range n.Classes {
		idx[c] = i
	}
	wr, wn := e.wRF, e.wNet
	pe := make([]float64, len(pr))
	for i, c := range m.Classes {
		pe[i] = wr * pr[i]
		if j, ok := idx[c]; ok {
			pe[i] += wn * pn[j]
		}
		pe[i] /= wr + wn
	}
	ens, conf = best(pe, m.Classes)
	return
}

func (e *Engine) classify(f *flow.Flow) {
	n := f.PPICount()
	f.LastClassifiedAt = n
	k := f.Key
	name, src := f.Names.Best()
	if d, ok := e.dns[k.Server]; ok && f.Names.DNS == "" {
		f.Names.DNS = d.name
		name, src = f.Names.Best()
	}
	var rf, net, ens string
	var conf float64
	if n >= 3 && !f.Names.Proxied {
		rf, net, ens, conf = e.predict(f.Features())
	}
	if name != "" {
		app := label.FromName(name)
		f.Category, f.How, f.Confidence, f.LabelCat = app, "name:"+src, 1, app
		f.Strong = src == "sni" || src == "host"
		if f.Strong && !f.Names.Proxied {
			// training samples at the same horizons the router predicts at
			for hi, h := range flow.Horizons {
				if n >= h && f.Sampled&(1<<hi) == 0 {
					f.Sampled |= 1 << hi
					e.addLocal(ml.Sample{X: f.FeaturesAt(h), Label: app, Source: "local"})
				}
			}
			// live agreement: only for apps the models know
			if ens != "" && n >= flow.Horizons[0] {
				if m := e.model.Load(); m != nil && contains(m.Classes, app) {
					e.live["rf"].add(rf == app)
					e.live["ens"].add(ens == app)
					if net != "" {
						e.live["net"].add(net == app)
					}
					a := e.agree[app]
					a[1]++
					if ens == app {
						a[0]++
					}
					e.agree[app] = a
				}
			}
		}
		return
	}
	if app := label.FromAddress(k.Server, k.Proto == 17, k.SPort); app != "" {
		f.Category, f.How, f.Confidence = app, "address", 0.9
		return
	}
	if b := udpBehaviour(f); b != "" {
		f.Category, f.How, f.Confidence = b, "behaviour", 0.7
		return
	}
	if ens != "" && conf >= e.cfg.MinConf {
		f.Category, f.How, f.Confidence = ens, "model", conf
		return
	}
	f.Category, f.How, f.Confidence = label.AppUnknown, "none", conf
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (e *Engine) addLocal(s ml.Sample) {
	e.ds.Add(s)
	e.newLocal++
	e.queueMu.Lock()
	if len(e.queue) < 5000 {
		e.queue = append(e.queue, s)
	}
	e.queueMu.Unlock()
}

// finish handles a flow that ended: a training sample for short labelled
// flows that never reached the first horizon, then cleanup.
func (e *Engine) finish(k flow.Key, f *flow.Flow) {
	if f.Strong && !f.Names.Proxied && f.LabelCat != "" && f.Sampled == 0 && f.PPICount() >= 4 {
		e.addLocal(ml.Sample{X: f.Features(), Label: f.LabelCat, Source: "local"})
	}
	delete(e.flows, k)
	delete(e.clhbuf, k)
}

// Tick runs every few seconds on the engine goroutine: accounting, expiry,
// periodic re-classification, snapshot publication.
func (e *Engine) Tick(now time.Time) {
	dt := now.Sub(e.lastTick).Seconds()
	if e.lastTick.IsZero() || dt <= 0 {
		dt = 5
	}
	e.lastTick = now
	day := now.Format("2006-01-02")
	if day != e.day {
		e.today = map[string]*acct{}
		e.todayCli = map[string]map[string]float64{}
		e.day = day
	}
	e.tickRate = map[string]*acct{}
	e.tickCli = map[string]map[string]*acct{}
	bucket := now.Unix() - now.Unix()%300
	if len(e.hist) == 0 || e.hist[len(e.hist)-1].T != bucket {
		e.hist = append(e.hist, histBucket{T: bucket, Apps: map[string]float64{}})
		if len(e.hist) > 288 {
			e.hist = e.hist[len(e.hist)-288:]
		}
	}
	hb := e.hist[len(e.hist)-1].Apps
	flowsPerApp := map[string]int{}
	for k, f := range e.flows {
		idle := now.Sub(f.Last)
		limit := 120 * time.Second
		if k.Proto == 17 {
			limit = 60 * time.Second
		}
		if (f.Closed && idle > 5*time.Second) || idle > limit {
			e.account(f, hb)
			e.finish(k, f)
			continue
		}
		if f.Category == "" || (f.How == "model" || f.How == "none") && now.Sub(f.First) > 10*time.Second && f.PPICount() > f.LastClassifiedAt {
			e.classify(f)
		}
		e.account(f, hb)
		if idle < 15*time.Second {
			flowsPerApp[f.Category]++
		}
	}
	for a, v := range e.tickRate {
		v.Down /= dt
		v.Up /= dt
		_ = a
	}
	for _, m := range e.tickCli {
		for _, v := range m {
			v.Down /= dt
			v.Up /= dt
		}
	}
	for ip, t := range e.dns {
		if now.After(t.exp) {
			delete(e.dns, ip)
		}
	}
	e.publish(now, flowsPerApp)
}

func (e *Engine) account(f *flow.Flow, hb map[string]float64) {
	if f.AcctUp+f.AcctDown == 0 {
		return
	}
	app := f.Category
	if app == "" {
		app = label.AppUnknown
	}
	cli := f.Key.Client.String()
	add := func(m map[string]*acct, key string) {
		a := m[key]
		if a == nil {
			a = &acct{}
			m[key] = a
		}
		a.Down += float64(f.AcctDown)
		a.Up += float64(f.AcctUp)
	}
	add(e.tickRate, app)
	add(e.today, app)
	if e.tickCli[cli] == nil {
		e.tickCli[cli] = map[string]*acct{}
	}
	add(e.tickCli[cli], app)
	if e.todayCli[cli] == nil {
		e.todayCli[cli] = map[string]float64{}
	}
	e.todayCli[cli][app] += float64(f.AcctDown + f.AcctUp)
	hb[app] += float64(f.AcctDown + f.AcctUp)
	f.AcctUp, f.AcctDown = 0, 0
}

// ---------------- snapshot for the API ----------------

// RunningApp is one entry of the "running now" list.
type RunningApp struct {
	App      string   `json:"app"`
	Category string   `json:"category"`
	Activity string   `json:"activity"`
	Down     float64  `json:"down_bps"`
	Up       float64  `json:"up_bps"`
	Flows    int      `json:"flows"`
	Clients  []string `json:"clients"`
	How      string   `json:"how"`
}

// Snapshot is what the API serves.
type Snapshot struct {
	Time     int64                         `json:"time"`
	Running  []RunningApp                  `json:"running"`
	Today    map[string]float64            `json:"today_bytes"`
	TodayCli map[string]map[string]float64 `json:"today_by_client"`
	History  []histBucket                  `json:"history"`
	Flows    []FlowView                    `json:"flows"`
	Stats    map[string]interface{}        `json:"stats"`
}

// FlowView is a privacy-friendly view of an active flow (no names/addresses).
type FlowView struct {
	Client     string       `json:"client"`
	App        string       `json:"app"`
	Activity   string       `json:"activity"`
	How        string       `json:"how"`
	Confidence float64      `json:"confidence"`
	Proto      string       `json:"proto"`
	Down       float64      `json:"down_bps"`
	Up         float64      `json:"up_bps"`
	Behaviour  flow.Summary `json:"behaviour"`
}

// Activity turns an app into a human activity label.
func Activity(app string) string {
	switch label.CategoryOf(app) {
	case label.CatVideo:
		return "Video streaming"
	case label.CatGaming:
		return "Gaming"
	case label.CatMusic:
		return "Music streaming"
	case label.CatSocial:
		return "Social media"
	case label.CatChat:
		return "Chat & calls"
	case label.CatAdult:
		return "Adult content"
	case label.CatUpdates:
		return "Downloads & sync"
	case label.CatDev:
		return "AI & dev tools"
	case label.CatAds:
		return "Ads & tracking"
	case label.CatWeb:
		return "Web browsing"
	}
	switch app {
	case "Online game (unknown)":
		return "Gaming"
	case "Voice chat (unknown app)":
		return "Chat & calls"
	}
	return "Other"
}

func (e *Engine) clientName(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	e.hostsMu.Lock()
	defer e.hostsMu.Unlock()
	if h, ok := e.hosts[a]; ok && h.Name != "" {
		return h.Name
	}
	if e.isRouter(a) {
		return "router (proxy)"
	}
	return ip
}

func (e *Engine) publish(now time.Time, flowsPerApp map[string]int) {
	var run []RunningApp
	for app, r := range e.tickRate {
		if r.Down+r.Up < 300 && flowsPerApp[app] == 0 {
			continue
		}
		ra := RunningApp{App: app, Category: label.CategoryOf(app), Activity: Activity(app), Down: math.Round(r.Down * 8), Up: math.Round(r.Up * 8), Flows: flowsPerApp[app]}
		for cli, m := range e.tickCli {
			if a := m[app]; a != nil && a.Down+a.Up > 0 {
				ra.Clients = append(ra.Clients, e.clientName(cli))
			}
		}
		sort.Strings(ra.Clients)
		run = append(run, ra)
	}
	sort.Slice(run, func(i, j int) bool { return run[i].Down+run[i].Up > run[j].Down+run[j].Up })
	// methods used per app (for transparency)
	how := map[string]map[string]int{}
	var views []FlowView
	for _, f := range e.flows {
		if f.Category == "" {
			continue
		}
		h := strings.SplitN(f.How, ":", 2)[0]
		if how[f.Category] == nil {
			how[f.Category] = map[string]int{}
		}
		how[f.Category][h]++
		if now.Sub(f.Last) < 15*time.Second {
			proto := "TCP"
			if f.Key.Proto == 17 {
				proto = "UDP"
				if f.Key.SPort == 443 {
					proto = "QUIC"
				}
			}
			views = append(views, FlowView{Client: e.clientName(f.Key.Client.String()), App: f.Category, Activity: Activity(f.Category),
				How: h, Confidence: math.Round(f.Confidence*100) / 100, Proto: proto, Behaviour: f.Summarize()})
		}
	}
	for i := range run {
		best, bn := "", 0
		for h, n := range how[run[i].App] {
			if n > bn {
				best, bn = h, n
			}
		}
		run[i].How = best
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Behaviour.PPS > views[j].Behaviour.PPS })
	if len(views) > 40 {
		views = views[:40]
	}
	today := map[string]float64{}
	for a, v := range e.today {
		today[a] = v.Down + v.Up
	}
	tc := map[string]map[string]float64{}
	for cli, m := range e.todayCli {
		n := e.clientName(cli)
		if tc[n] == nil {
			tc[n] = map[string]float64{}
		}
		for a, v := range m {
			tc[n][a] += v
		}
	}
	agree := map[string]interface{}{}
	var ag, at int
	for a, v := range e.agree {
		agree[a] = map[string]int{"agree": v[0], "labelled": v[1]}
		ag += v[0]
		at += v[1]
	}
	counts, sources := e.ds.Counts()
	e.evalMu.Lock()
	ev, evN, evE := e.eval, e.evalNet, e.evalEns
	ta := e.trainedAt
	hist := append([]Round(nil), e.history...)
	e.evalMu.Unlock()
	liveOut := map[string]interface{}{}
	for k, v := range e.live {
		liveOut[k] = map[string]interface{}{"agree": v.Agree, "total": v.Total, "recent": math.Round(v.EWMA*1000) / 1000}
	}
	if n := e.net.Load(); n != nil {
		liveOut["net_steps"] = n.Steps
	}
	stats := map[string]interface{}{
		"packets": e.pkts, "active_flows": len(e.flows), "dns_cache": len(e.dns),
		"model_loaded": e.model.Load() != nil, "training": e.training.Load(), "trained_at": ta.Unix(),
		"holdout_accuracy": ev.Accuracy, "holdout_n": ev.N, "per_class": ev.PerClass,
		"samples_per_class": counts, "samples_by_source": sources, "new_local_samples": e.newLocal,
		"live_agreement": agree, "live_agreement_total": map[string]int{"agree": ag, "labelled": at},
		"min_confidence": e.cfg.MinConf,
		"net_loaded":     e.net.Load() != nil,
		"net_holdout":    evN.Accuracy, "ens_holdout": evE.Accuracy,
		"local_holdout": map[string]float64{"rf": ev.LocalAccuracy, "net": evN.LocalAccuracy, "ens": evE.LocalAccuracy, "n": float64(ev.LocalN)},
		"live":          liveOut, "weights": map[string]float64{"rf": e.wRF, "net": e.wNet},
		"online_updates": e.online.Load(), "history": hist,
		"activities": activities(),
	}
	if m := e.model.Load(); m != nil {
		type fi struct {
			Name string  `json:"name"`
			V    float64 `json:"importance"`
		}
		var imp []fi
		for i, v := range m.Importance {
			imp = append(imp, fi{m.Features[i], math.Round(v*1000) / 1000})
		}
		sort.Slice(imp, func(i, j int) bool { return imp[i].V > imp[j].V })
		if len(imp) > 15 {
			imp = imp[:15]
		}
		stats["top_features"] = imp
		stats["classes"] = m.Classes
	}
	e.snapMu.Lock()
	e.snap = Snapshot{Time: now.Unix(), Running: run, Today: today, TodayCli: tc, History: append([]histBucket(nil), e.hist...), Flows: views, Stats: stats}
	e.snapMu.Unlock()
}

func activities() map[string]string {
	m := map[string]string{}
	for _, a := range label.Apps {
		m[a.Name] = Activity(a.Name)
	}
	for _, a := range []string{label.AppWeb, label.AppUnknown, label.AppDNS, label.AppVPN, "Online game (unknown)", "Voice chat (unknown app)"} {
		m[a] = Activity(a)
	}
	return m
}

// Snap returns the latest snapshot.
func (e *Engine) Snap() Snapshot {
	e.snapMu.RLock()
	defer e.snapMu.RUnlock()
	return e.snap
}

// ---------------- training ----------------

// MaybeTrain retrains forest + network in the background when due.
func (e *Engine) MaybeTrain(force bool) {
	due := e.model.Load() == nil || e.net.Load() == nil || force ||
		(time.Since(e.trainedAt) > e.cfg.RetrainMin && e.newLocal >= 100)
	if !due || e.training.Load() {
		return
	}
	samples := e.ds.Snapshot(25)
	if len(samples) < 200 {
		return
	}
	e.training.Store(true)
	newLocal := e.newLocal
	go func() {
		defer e.training.Store(false)
		t0 := time.Now()
		p := ml.DefaultParams(len(flow.FeatureNames))
		r := ml.TrainBoth(samples, flow.FeatureNames, p, 3)
		e.model.Store(r.Forest)
		e.net.Store(r.Net)
		e.queueMu.Lock()
		e.queue = nil // already contained in this training run
		e.queueMu.Unlock()
		e.evalMu.Lock()
		e.eval, e.evalNet, e.evalEns, e.trainedAt = r.EvalRF, r.EvalNet, r.EvalEns, time.Now()
		e.wRF, e.wNet = 1-r.NetW, r.NetW
		e.newLocal -= newLocal
		e.history = append(e.history, Round{T: time.Now().Unix(), Kind: "retrain", Samples: r.TrainN, Local: r.LocalN, Classes: len(r.Forest.Classes),
			AccRF: r.EvalRF.Accuracy, AccNet: r.EvalNet.Accuracy, AccEns: r.EvalEns.Accuracy,
			LocalAccRF: r.EvalRF.LocalAccuracy, LocalAccNt: r.EvalNet.LocalAccuracy, LocalAccEn: r.EvalEns.LocalAccuracy,
			LiveRF: e.live["rf"].EWMA, LiveNet: e.live["net"].EWMA, LiveEns: e.live["ens"].EWMA, Seconds: time.Since(t0).Seconds()})
		e.saveHistoryLocked()
		e.evalMu.Unlock()
		r.Forest.Save(filepath.Join(e.cfg.DataDir, "model.json"))
		r.Net.Save(filepath.Join(e.cfg.DataDir, "net.json"))
		b, _ := json.Marshal(r.EvalRF)
		os.WriteFile(filepath.Join(e.cfg.DataDir, "eval.json"), b, 0644)
		e.ds.SaveLocalCSV(filepath.Join(e.cfg.DataDir, "local.csv.gz"))
		log.Printf("trained on %d samples (%d local, %d classes) in %s: hold-out rf %.3f net %.3f ens %.3f | local rf %.3f net %.3f ens %.3f",
			r.TrainN, r.LocalN, len(r.Forest.Classes), time.Since(t0).Round(time.Second), r.EvalRF.Accuracy, r.EvalNet.Accuracy, r.EvalEns.Accuracy,
			r.EvalRF.LocalAccuracy, r.EvalNet.LocalAccuracy, r.EvalEns.LocalAccuracy)
	}()
}

// OnlineStep updates the network with newly labelled local flows (mixed
// with class-balanced replay from the pool) and re-weights the ensemble from
// live agreement. Call about once a minute.
func (e *Engine) OnlineStep() {
	e.updateWeights()
	n := e.net.Load()
	if n == nil || e.training.Load() {
		return
	}
	e.queueMu.Lock()
	fresh := e.queue
	e.queue = nil
	e.queueMu.Unlock()
	if len(fresh) == 0 {
		return
	}
	go func() {
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		c := n.Clone()
		steps := len(fresh)/8 + 3
		for i := 0; i < steps; i++ {
			batch := make([]ml.Sample, 0, 48)
			for j := 0; j < 16; j++ {
				batch = append(batch, fresh[rng.Intn(len(fresh))])
			}
			batch = append(batch, e.ds.Random(32, rng)...)
			c.Step(batch, nil, 5e-4)
		}
		if !e.training.Load() {
			e.net.Store(c)
			e.online.Add(1)
		}
		if e.online.Load()%10 == 0 {
			c.Save(filepath.Join(e.cfg.DataDir, "net.json"))
			e.evalMu.Lock()
			e.history = append(e.history, Round{T: time.Now().Unix(), Kind: "online", Samples: len(fresh),
				LiveRF: e.live["rf"].EWMA, LiveNet: e.live["net"].EWMA, LiveEns: e.live["ens"].EWMA})
			e.saveHistoryLocked()
			e.evalMu.Unlock()
		}
	}()
}

// updateWeights sets ensemble weights proportional to each model's recent
// live agreement (needs some evidence before moving away from the prior).
func (e *Engine) updateWeights() {
	r, n := e.live["rf"], e.live["net"]
	if r.Total < 50 || n.Total < 50 {
		return
	}
	// the model that agrees more with names seen on this network gets more weight
	d := n.EWMA - r.EWMA
	w := e.wNet + 0.05*d
	if w < 0 {
		w = 0
	} else if w > 0.7 {
		w = 0.7
	}
	e.wRF, e.wNet = 1-w, w
}

func (e *Engine) saveHistoryLocked() {
	if len(e.history) > 600 {
		e.history = e.history[len(e.history)-600:]
	}
	b, _ := json.Marshal(e.history)
	os.WriteFile(filepath.Join(e.cfg.DataDir, "history.json"), b, 0644)
}

// ---------------- hosts & persistence ----------------

func (e *Engine) refreshHosts() {
	h := map[netip.Addr]hostInfo{}
	if fh, err := os.Open(e.cfg.Leases); err == nil {
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) >= 4 {
				if a, err := netip.ParseAddr(f[2]); err == nil {
					n := f[3]
					if n == "*" {
						n = ""
					}
					h[a] = hostInfo{MAC: strings.ToLower(f[1]), Name: n}
				}
			}
		}
		fh.Close()
	}
	if e.cfg.NamesFile != "" {
		var names map[string]struct {
			Name  string `json:"name"`
			LName string `json:"lname"`
		}
		if b, err := os.ReadFile(e.cfg.NamesFile); err == nil && json.Unmarshal(b, &names) == nil {
			for a, hi := range h {
				if n, ok := names[hi.MAC]; ok && n.Name != "" {
					hi.Name = n.Name
					h[a] = hi
				}
			}
		}
	}
	e.hostsMu.Lock()
	e.hosts = h
	e.hostsMu.Unlock()
}

// RefreshHosts reloads client names (call periodically).
func (e *Engine) RefreshHosts() { e.refreshHosts() }

type persisted struct {
	Day      string                        `json:"day"`
	Today    map[string]*acct              `json:"today"`
	TodayCli map[string]map[string]float64 `json:"today_cli"`
	Hist     []histBucket                  `json:"hist"`
	Agree    map[string][2]int             `json:"agree"`
}

// SaveState persists counters (call periodically and on exit).
func (e *Engine) SaveState() {
	b, _ := json.Marshal(persisted{e.day, e.today, e.todayCli, e.hist, e.agree})
	p := filepath.Join(e.cfg.DataDir, "state.json")
	os.WriteFile(p+".tmp", b, 0644)
	os.Rename(p+".tmp", p)
	if e.newLocal > 0 {
		e.ds.SaveLocalCSV(filepath.Join(e.cfg.DataDir, "local.csv.gz"))
	}
}

func (e *Engine) loadState() {
	b, err := os.ReadFile(filepath.Join(e.cfg.DataDir, "state.json"))
	if err != nil {
		return
	}
	var s persisted
	if json.Unmarshal(b, &s) != nil {
		return
	}
	e.day, e.hist = s.Day, s.Hist
	if s.Today != nil {
		e.today = s.Today
	}
	if s.TodayCli != nil {
		e.todayCli = s.TodayCli
	}
	if s.Agree != nil {
		e.agree = s.Agree
	}
}
