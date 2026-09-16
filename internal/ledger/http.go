package ledger

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Raven020/stableBank/internal/httpx"
	"github.com/Raven020/stableBank/internal/store"
)

// RegisterRoutes wires every ledger HTTP endpoint into mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ledger/accounts", s.handleListAccounts)
	mux.HandleFunc("GET /ledger/accounts/{id}", s.handleGetAccount)
	mux.HandleFunc("GET /ledger/accounts/{id}/balance", s.handleGetBalance)
	mux.HandleFunc("GET /ledger/transactions", s.handleListTransactions)
	mux.HandleFunc("GET /ledger/transactions/{id}", s.handleGetTransaction)
	mux.HandleFunc("POST /ledger/transactions/{id}/settle", s.handleSettle)
	mux.HandleFunc("POST /ledger/transactions/{id}/finalize", s.handleFinalize)
	mux.HandleFunc("GET /ledger/balances", s.handleBalances)
	mux.HandleFunc("GET /ledger/fx/quote", s.handleFXQuote)
	mux.HandleFunc("GET /ledger/currencies", s.handleCurrencies)
}

func (s *Service) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.ListAccounts(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, accounts)
}

func (s *Service) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	acc, err := s.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "account not found", nil)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, acc)
}

func (s *Service) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	bal, err := s.Balance(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "account not found", nil)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, bal)
}

func (s *Service) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	f := TxFilter{
		AccountID: r.URL.Query().Get("account_id"),
		Kind:      r.URL.Query().Get("kind"),
	}
	txs, err := s.ListTransactions(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, txs)
}

func (s *Service) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	tx, err := s.GetTransaction(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "transaction not found", nil)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tx)
}

func (s *Service) handleSettle(w http.ResponseWriter, r *http.Request) {
	tx, err := s.Settle(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeTxError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tx)
}

func (s *Service) handleFinalize(w http.ResponseWriter, r *http.Request) {
	tx, err := s.Finalize(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeTxError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tx)
}

func (s *Service) writeTxError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "transaction not found", nil)
		return
	}
	httpx.WriteError(w, http.StatusConflict, err.Error(), nil)
}

func (s *Service) handleBalances(w http.ResponseWriter, r *http.Request) {
	balances, err := s.Balances(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, balances)
}

func (s *Service) handleFXQuote(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	amount, err := strconv.ParseInt(r.URL.Query().Get("amount_minor"), 10, 64)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid amount_minor", nil)
		return
	}
	quote, err := s.Quote(from, to, amount)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, quote)
}

func (s *Service) handleCurrencies(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, Currencies)
}
