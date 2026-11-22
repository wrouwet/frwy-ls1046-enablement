// flowsense: encrypted-traffic application classifier for OpenWrt routers.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/api"
	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/capture"
	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/engine"
)

func main() {
	iface := flag.String("i", "br-lan", "LAN interface to observe")
	nets := flag.String("lan", "192.168.50.0/24", "comma-separated client networks")
	router := flag.String("router", "192.168.50.1", "comma-separated router LAN addresses")
	proxy := flag.Int("proxy-port", 0, "local HTTP proxy port whose CONNECT tunnels should be classified (0=off)")
	listen := flag.String("listen", "127.0.0.1:8091", "API listen address")
	data := flag.String("data", "/etc/flowsense", "persistent data directory")
	seed := flag.String("seed", "/usr/share/flowsense/seed.csv.gz", "seed dataset (public data)")
	leases := flag.String("leases", "/tmp/dhcp.leases", "dnsmasq leases file")
	names := flag.String("names", "", "optional JSON with user-given device names {mac:{name}}")
	minConf := flag.Float64("min-conf", 0.55, "minimum model confidence to report a prediction")
	retrain := flag.Duration("retrain", 30*time.Minute, "minimum time between retrainings")
	tz := flag.String("tz", "", "time zone for daily totals (e.g. America/Chicago)")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("flowsense: ")
	if *tz != "" {
		if loc, err := time.LoadLocation(*tz); err == nil {
			time.Local = loc
		} else {
			log.Printf("tz: %v", err)
		}
	}

	cfg := engine.Config{DataDir: *data, SeedCSV: *seed, MinConf: *minConf, Leases: *leases, NamesFile: *names,
		ProxyPort: uint16(*proxy), RetrainMin: *retrain}
	for _, s := range strings.Split(*nets, ",") {
		cfg.LocalNets = append(cfg.LocalNets, netip.MustParsePrefix(strings.TrimSpace(s)))
	}
	for _, s := range strings.Split(*router, ",") {
		cfg.RouterIPs = append(cfg.RouterIPs, netip.MustParseAddr(strings.TrimSpace(s)))
	}
	e := engine.New(cfg)

	src, err := capture.Open(*iface)
	if err != nil {
		log.Fatal(err)
	}
	pk := make(chan capture.Packet, 8192)
	go func() {
		for {
			p, ok, err := src.Next(2048)
			if err != nil {
				log.Fatal(err)
			}
			if ok {
				select {
				case pk <- p:
				default: // engine overloaded: drop rather than block the socket
				}
			}
		}
	}()
	go func() {
		srv := &http.Server{Addr: *listen, Handler: api.Handler(e), ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
	}()
	log.Printf("capturing on %s, API on %s", *iface, *listen)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(5 * time.Second)
	hosts := time.NewTicker(30 * time.Second)
	save := time.NewTicker(5 * time.Minute)
	e.MaybeTrain(false)
	for {
		select {
		case p := <-pk:
			e.Handle(&p)
		case now := <-tick.C:
			e.Tick(now)
		case <-hosts.C:
			e.RefreshHosts()
			e.MaybeTrain(false)
		case <-save.C:
			e.SaveState()
		case <-sig:
			e.SaveState()
			log.Print("stopped")
			return
		}
	}
}
