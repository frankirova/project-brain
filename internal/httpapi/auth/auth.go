// Package auth provides HTTP middleware for bearer-token authentication.
package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Middleware returns an http middleware that requires a valid bearer
// token. The expected token is compared in constant time. Requests with
// missing or invalid tokens get 401 Unauthorized with a JSON body.
func Middleware(expectedToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expectedToken == "" {
				// No token configured — auth disabled. Open access.
				next.ServeHTTP(w, r)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				unauthorized(w, "missing Authorization header")
				return
			}

			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				unauthorized(w, "Authorization must use Bearer scheme")
				return
			}

			token := strings.TrimSpace(authHeader[len(prefix):])
			if token == "" {
				unauthorized(w, "empty bearer token")
				return
			}

			if !constantTimeEqual(token, expectedToken) {
				unauthorized(w, "invalid bearer token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// errorBody is the JSON wire shape for 401 responses. Built with
// json.Marshal so user-supplied strings (header values) cannot
// inject JSON-breaking characters.
type errorBody struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func unauthorized(w http.ResponseWriter, msg string) {
	body, _ := json.Marshal(errorBody{
		Error:   "Unauthorized",
		Code:    "AUTH_REQUIRED",
		Message: msg,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write(body)
}

// constantTimeEqual compares two strings in constant time to avoid
// timing-based token leakage.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
