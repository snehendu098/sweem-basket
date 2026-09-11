// Package httpx holds JSON response helpers and the shared error envelope.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Envelope is the shape of every response body.
type Envelope struct {
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "err", err)
	}
}

func OK(w http.ResponseWriter, data any) { JSON(w, http.StatusOK, Envelope{Data: data}) }

func Fail(w http.ResponseWriter, status int, code, msg string) {
	JSON(w, status, Envelope{Error: &Error{Code: code, Message: msg}})
}
