package simulate

import (
	"errors"
	"net/http"

	"github.com/Raven020/stableBank/internal/account"
	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/loan"
	"github.com/Raven020/stableBank/internal/store"
)

// RegisterRoutes wires every simulate HTTP endpoint into mux.
//
// POST /simulate/purchase and POST /simulate/purchases/bulk are already
// registered by internal/account's Service.RegisterRoutes (they existed
// before this package did, per CONTRACTS.md's D4 section) — this package
// must not register them again, since net/http's ServeMux panics on a
// duplicate pattern.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /simulate/advance-time", s.handleAdvanceTime)
	mux.HandleFunc("POST /simulate/trigger-default", s.handleTriggerDefault)
	mux.HandleFunc("POST /simulate/run-scenario", s.handleRunScenario)
	mux.HandleFunc("GET /simulate/clock", s.handleClock)
	mux.HandleFunc("GET /simulate/presets", s.handlePresets)
}

func (s *Service) handleAdvanceTime(w http.ResponseWriter, r *http.Request) {
	var req AdvanceTimeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	result, err := s.AdvanceTime(r.Context(), req)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) handleTriggerDefault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LoanID string `json:"loan_id"`
		Reason string `json:"reason,omitempty"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	loanID, err := s.resolveLoanID(r.Context(), req.LoanID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	result, err := s.TriggerDefault(r.Context(), loanID, req.Reason)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) handleRunScenario(w http.ResponseWriter, r *http.Request) {
	var sc Scenario
	if err := httpx.Decode(r, &sc); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	result, err := s.RunScenario(r.Context(), sc)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) handleClock(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.Clock())
}

func (s *Service) handlePresets(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"presets": Presets()})
}

// writeServiceError maps a Service/loan/account error to the standard
// httpx error envelope, per CONTRACTS.md's status-code conventions (400
// validation, 404 not found, 409 conflict/decline, 500 otherwise).
func writeServiceError(w http.ResponseWriter, err error) {
	var verr *ValidationError
	if errors.As(err, &verr) {
		httpx.WriteError(w, http.StatusBadRequest, err.Error(), verr.Errors)
		return
	}
	var averr *account.ValidationError
	if errors.As(err, &averr) {
		httpx.WriteError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}

	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, loan.ErrApplicantNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, loan.ErrNotApproved), errors.Is(err, loan.ErrInsufficientFunds), errors.Is(err, account.ErrDeclined):
		httpx.WriteError(w, http.StatusConflict, err.Error(), nil)
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
	}
}
