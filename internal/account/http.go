package account

import (
	"errors"
	"net/http"

	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/ledger"
	"github.com/Raven020/stableBank/internal/store"
)

// RegisterRoutes wires every account HTTP endpoint into mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /simulate/purchase", s.handlePurchase)
	mux.HandleFunc("POST /simulate/purchases/bulk", s.handleBulkPurchases)
	mux.HandleFunc("GET /accounts", s.handleListAccounts)
	mux.HandleFunc("GET /accounts/{id}", s.handleGetAccount)
	mux.HandleFunc("POST /accounts/{id}/deposit", s.handleDeposit)
	mux.HandleFunc("GET /accounts/{id}/purchases", s.handleListPurchases)
}

func (s *Service) handlePurchase(w http.ResponseWriter, r *http.Request) {
	var req PurchaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	receipt, err := s.Purchase(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrDeclined) {
			httpx.WriteError(w, http.StatusConflict, "purchase declined", receipt)
			return
		}
		var verr *ValidationError
		if errors.As(err, &verr) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "account not found", nil)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, receipt)
}

func (s *Service) handleBulkPurchases(w http.ResponseWriter, r *http.Request) {
	var req BulkRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	receipts, err := s.BulkRandomPurchases(r.Context(), req)
	if err != nil {
		var verr *ValidationError
		if errors.As(err, &verr) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "account not found", nil)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, receipts)
}

func (s *Service) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	views, err := s.ListAccounts(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, views)
}

func (s *Service) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	view, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "account not found", nil)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

// depositRequest is the POST /accounts/{id}/deposit body: amount is an
// exact decimal string ("100.00") in the given currency.
type depositRequest struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

func (s *Service) handleDeposit(w http.ResponseWriter, r *http.Request) {
	var req depositRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body", nil)
		return
	}
	amt, err := ledger.ParseAmount(req.Currency, req.Amount)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	tx, err := s.Deposit(r.Context(), r.PathValue("id"), amt)
	if err != nil {
		var verr *ValidationError
		if errors.As(err, &verr) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "account not found", nil)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tx)
}

func (s *Service) handleListPurchases(w http.ResponseWriter, r *http.Request) {
	receipts, err := s.ListPurchases(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, receipts)
}
