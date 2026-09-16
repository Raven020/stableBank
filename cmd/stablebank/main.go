// Command stablebank is the single deployable binary for the
// intelligence-first banking PoC. All wiring lives here; domain packages
// never import each other's constructors.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/memstore"
)

func main() {
	ctx := context.Background()
	addr := getenv("ADDR", ":8080")

	start, err := time.Parse(time.RFC3339, getenv("SIM_START", "2026-09-01T09:00:00Z"))
	if err != nil {
		log.Fatalf("bad SIM_START: %v", err)
	}
	clock := simclock.New(start)

	var st store.Store = memstore.New()
	// pgstore is wired in once D3 lands: if DATABASE_URL is set, use it.
	_ = ctx

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]any{"status": "ok", "sim_now": clock.Now()})
	})
	defer st.Close()

	log.Printf("stablebank listening on %s (sim clock %s)", addr, clock.Now().Format(time.RFC3339))
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
