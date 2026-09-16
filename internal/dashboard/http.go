package dashboard

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Raven020/stableBank/internal/httpx"
)

// RegisterRoutes wires the dashboard's read-only HTTP surface.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard/summary", s.handleSummary)
	mux.HandleFunc("GET /dashboard/ledger-book", s.handleLedgerBook)
	mux.HandleFunc("GET /dashboard/flows", s.handleFlows)
	mux.HandleFunc("GET /dashboard/ratios", s.handleRatios)
}

func (s *Service) handleSummary(w http.ResponseWriter, r *http.Request) {
	out, err := s.Summary(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard: building summary failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleLedgerBook(w http.ResponseWriter, r *http.Request) {
	out, err := s.LedgerBook(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard: building ledger book failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleRatios(w http.ResponseWriter, r *http.Request) {
	out, err := s.Ratios(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard: computing ratios failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleFlows supports window_days=N (default 30, ignored if from/to given)
// or explicit from=/to= RFC3339 timestamps.
func (s *Service) handleFlows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := s.clock.Now()

	var from, to time.Time
	var err error
	if v := q.Get("from"); v != "" {
		from, err = time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "dashboard: invalid from", err.Error())
			return
		}
	}
	if v := q.Get("to"); v != "" {
		to, err = time.Parse(time.RFC3339, v)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "dashboard: invalid to", err.Error())
			return
		}
	}
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		days := 30
		if v := q.Get("window_days"); v != "" {
			days, err = strconv.Atoi(v)
			if err != nil || days <= 0 {
				httpx.WriteError(w, http.StatusBadRequest, "dashboard: invalid window_days", v)
				return
			}
		}
		from = to.AddDate(0, 0, -days)
	}

	out, err := s.Flows(r.Context(), from, to)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "dashboard: computing flows failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
