// Package httpx holds the tiny JSON helpers shared by every HTTP handler so
// that error and success envelopes look identical across domains.
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
)

// ErrorResponse is the standard error envelope.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// WriteError writes the standard error envelope.
func WriteError(w http.ResponseWriter, status int, msg string, details any) {
	WriteJSON(w, status, ErrorResponse{Error: msg, Details: details})
}

// Decode reads a JSON body into v, rejecting unknown fields.
func Decode(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty request body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
