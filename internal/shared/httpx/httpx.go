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

// CORS allows browser clients to call this API. Origins come from the
// CORS_ORIGINS env var (comma-separated); "*" allows any origin.
//
// Credentials are never allowed: these APIs authenticate with a bearer token
// the client already holds, not with cookies, so echoing an arbitrary origin
// back with credentials would be a needless hole.
func CORS(origins []string, next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(origins))
	any := false
	for _, o := range origins {
		if o == "*" {
			any = true
		}
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && (any || allowed[origin]) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
