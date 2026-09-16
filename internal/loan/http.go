package loan

import (
	"errors"
	"net/http"

	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/store"
)

// RegisterRoutes wires every loan HTTP endpoint into mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /loan/underwrite", s.handleUnderwrite)
	mux.HandleFunc("POST /loan/originate", s.handleOriginate)
	mux.HandleFunc("GET /loan/applicants", s.handleApplicants)
	mux.HandleFunc("GET /loans", s.handleListLoans)
	mux.HandleFunc("GET /loans/forecast/bank", s.handleBankForecast)
	mux.HandleFunc("GET /loans/{id}", s.handleGetLoan)
	mux.HandleFunc("GET /loans/{id}/schedule", s.handleSchedule)
	mux.HandleFunc("GET /loans/{id}/forecast", s.handleCustomerForecast)
	mux.HandleFunc("POST /loans/{id}/what-if", s.handleWhatIf)
	mux.HandleFunc("POST /loans/{id}/repay", s.handleRepay)
	mux.HandleFunc("POST /loans/{id}/miss-payment", s.handleMissPayment)
	mux.HandleFunc("POST /loans/{id}/default", s.handleDefault)
}

// decodeBody decodes a JSON body into v when present; every loan POST
// endpoint accepts an empty body (all fields optional or supplied via the
// path), so a missing/empty body is not an error.
func decodeBody(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return httpx.Decode(r, v)
}

func (s *Service) handleUnderwrite(w http.ResponseWriter, r *http.Request) {
	var req UnderwriteRequest
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.ApplicantID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "applicant_id is required", nil)
		return
	}
	result, err := s.Underwrite(r.Context(), req)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) handleOriginate(w http.ResponseWriter, r *http.Request) {
	var req OriginateRequest
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if req.ApplicantID == "" {
		httpx.WriteError(w, http.StatusBadRequest, "applicant_id is required", nil)
		return
	}
	loan, err := s.Originate(r.Context(), req)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loan)
}

func (s *Service) handleApplicants(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.Applicants())
}

func (s *Service) handleListLoans(w http.ResponseWriter, r *http.Request) {
	loans, err := s.List(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loans)
}

func (s *Service) handleGetLoan(w http.ResponseWriter, r *http.Request) {
	loan, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loan)
}

func (s *Service) handleSchedule(w http.ResponseWriter, r *http.Request) {
	sched, err := s.Schedule(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sched)
}

func (s *Service) handleWhatIf(w http.ResponseWriter, r *http.Request) {
	var req WhatIfRequest
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	result, err := s.WhatIf(r.Context(), r.PathValue("id"), req)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) handleRepay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AmountCents int64 `json:"amount_cents,omitempty"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	loan, err := s.Repay(r.Context(), r.PathValue("id"), req.AmountCents)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loan)
}

func (s *Service) handleMissPayment(w http.ResponseWriter, r *http.Request) {
	loan, err := s.MissPayment(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loan)
}

func (s *Service) handleDefault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason,omitempty"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	loan, err := s.Default(r.Context(), r.PathValue("id"), req.Reason)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loan)
}

func (s *Service) handleBankForecast(w http.ResponseWriter, r *http.Request) {
	fc, err := s.BankForecast(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, fc)
}

func (s *Service) handleCustomerForecast(w http.ResponseWriter, r *http.Request) {
	fc, err := s.CustomerForecast(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, fc)
}

func (s *Service) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrApplicantNotFound), errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, ErrNotApproved), errors.Is(err, ErrInsufficientFunds):
		httpx.WriteError(w, http.StatusConflict, err.Error(), nil)
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
	}
}
