// Command stablebank is the single deployable binary for the
// intelligence-first banking PoC. All wiring lives here; domain packages
// never import each other's constructors.
//
// PoC-only surfaces: everything under /simulate/* and /demo/* exists purely
// to drive demos and must be excluded from any production build.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/memstore"
	"github.com/Raven020/stableBank/internal/store/pgstore"
	"github.com/Raven020/stableBank/migrations"
)

// ruleReader adapts rules.Engine to ledger.RuleReader.
type ruleReader struct{ e *rules.Engine }

func (r ruleReader) Get(ruleID string) (map[string]any, int, error) { return r.e.GetContent(ruleID) }

func main() {
	ctx := context.Background()
	addr := getenv("ADDR", ":8080")
	rulesDir := getenv("RULES_DIR", "rules")

	start, err := time.Parse(time.RFC3339, getenv("SIM_START", "2026-09-01T09:00:00Z"))
	if err != nil {
		log.Fatalf("bad SIM_START: %v", err)
	}
	clock := simclock.New(start)

	// Storage: Postgres when DATABASE_URL is set, otherwise in-memory.
	var st store.Store
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pg, err := pgstore.Open(ctx, dsn)
		if err != nil {
			log.Fatalf("postgres: %v", err)
		}
		if err := pg.Migrate(ctx, migrations.FS); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		st = pg
		log.Printf("storage: postgres")
	} else {
		st = memstore.New()
		log.Printf("storage: in-memory (set DATABASE_URL to use Postgres)")
	}
	defer st.Close()

	// Rules engine first: everything else consumes it.
	engine := rules.NewEngine(st, clock, rulesDir)
	engine.RegisterEvaluator("fx_policy", ledger.EvaluateFXPolicy)
	engine.RegisterEvaluator("depeg_policy", ledger.EvaluateDepegPolicy)
	if err := engine.LoadFromDisk(ctx); err != nil {
		log.Fatalf("rules: %v", err)
	}

	// Ledger with rule-driven mock FX rate.
	rates := ledger.NewRuleRateProvider(ruleReader{engine})
	rates.Clock = clock
	book := ledger.New(st, clock, rates)
	if err := book.EnsureSystemAccounts(ctx); err != nil {
		log.Fatalf("ledger: %v", err)
	}
	if err := book.SeedCapital(ctx, 25_000_000); err != nil { // USD 250,000.00 illustrative capital
		log.Fatalf("ledger seed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]any{"status": "ok", "sim_now": clock.Now()})
	})
	engine.RegisterRoutes(mux)
	book.RegisterRoutes(mux)

	log.Printf("stablebank listening on %s (sim clock %s)", addr, clock.Now().Format(time.RFC3339))
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
