// Package api serves the engine snapshot as JSON.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/wrouwet/frwy-ls1046-enablement/flowsense/internal/engine"
)

// Handler returns the HTTP handler.
func Handler(e *engine.Engine) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/api/all", func(w http.ResponseWriter, r *http.Request) { write(w, e.Snap()) })
	mux.HandleFunc("/api/now", func(w http.ResponseWriter, r *http.Request) {
		s := e.Snap()
		top := s.Running
		if len(top) > 10 {
			top = top[:10]
		}
		write(w, map[string]interface{}{"time": s.Time, "running": top})
	})
	mux.HandleFunc("/api/today", func(w http.ResponseWriter, r *http.Request) {
		s := e.Snap()
		write(w, map[string]interface{}{"time": s.Time, "today_bytes": s.Today, "today_by_client": s.TodayCli, "history": s.History})
	})
	mux.HandleFunc("/api/flows", func(w http.ResponseWriter, r *http.Request) { s := e.Snap(); write(w, s.Flows) })
	mux.HandleFunc("/api/model", func(w http.ResponseWriter, r *http.Request) { s := e.Snap(); write(w, s.Stats) })
	mux.HandleFunc("/api/retrain", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		e.MaybeTrain(true)
		write(w, map[string]bool{"ok": true})
	})
	return mux
}
