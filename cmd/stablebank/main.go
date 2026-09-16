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

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/dashboard"
	"github.com/Raven020/stableBank/internal/demo"
	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/loan"
	"github.com/Raven020/stableBank/internal/rules"
	"github.com/Raven020/stableBank/internal/simclock"
	"github.com/Raven020/stableBank/internal/simulate"
	"github.com/Raven020/stableBank/internal/store"
	"github.com/Raven020/stableBank/internal/store/memstore"
	"github.com/Raven020/stableBank/internal/store/pgstore"
	"github.com/Raven020/stableBank/migrations"
	"github.com/Raven020/stableBank/web"
)

// ruleReader adapts rules.Engine to ledger.RuleReader.
type ruleReader struct{ e *rules.Engine }

func (r ruleReader) Get(ruleID string) (map[string]any, int, error) { return r.e.GetContent(ruleID) }

func main() {
	ctx := context.Background()
	addr := getenv("ADDR", ":8080")
	rulesDir := getenv("RULES_DIR", "rules")
	demoPath := getenv("DEMO_SCENARIOS", "demo/demo-scenarios.yaml")

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
	engine.RegisterEvaluator("spend_waterfall", account.EvaluateWaterfall)
	engine.RegisterEvaluator("risk_scoring", loan.EvaluateRiskScore)
	engine.RegisterEvaluator("loan_underwriting", loan.EvaluateUnderwriting)
	engine.RegisterEvaluator("loan_servicing", loan.EvaluateServicing)
	engine.RegisterEvaluator("dashboard_thresholds", dashboard.EvaluateThresholds)
	engine.RegisterEvaluator("risk_weights", dashboard.EvaluateRiskWeights)
	engine.RegisterEvaluator("liquidity_stress", dashboard.EvaluateLiquidityStress)
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

	// Domain services. Seeding is idempotent so restarts against Postgres
	// do not duplicate the demo state.
	accounts := account.New(book, engine, clock)
	if err := accounts.Seed(ctx); err != nil {
		log.Fatalf("account seed: %v", err)
	}
	loans := loan.New(book, engine, st, clock)
	if err := loans.Seed(ctx); err != nil {
		log.Fatalf("loan seed: %v", err)
	}
	dash := dashboard.New(book, engine, loans, clock)
	sim := simulate.New(clock, loans, accounts, book) // PoC-only: /simulate/*

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]any{"status": "ok", "sim_now": clock.Now()})
	})
	engine.RegisterRoutes(mux)
	book.RegisterRoutes(mux)
	accounts.RegisterRoutes(mux)
	loans.RegisterRoutes(mux)
	dash.RegisterRoutes(mux)
	sim.RegisterRoutes(mux)
	// Demo Mode fires call_api steps against this same mux in-process.
	// PoC-only: /demo/*
	show := demo.New(engine, mux, clock, demoPath)
	if err := show.LoadError(); err != nil {
		log.Fatalf("demo scenarios: %v", err)
	}
	show.RegisterRoutes(mux)
	mux.Handle("/", web.Handler())

	log.Printf("stablebank listening on %s (sim clock %s)", addr, clock.Now().Format(time.RFC3339))
	log.Fatal(http.ListenAndServe(addr, mux))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
