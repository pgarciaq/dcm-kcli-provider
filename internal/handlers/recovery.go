package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
)

func PanicRecovery(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rvr := recover(); rvr != nil {
					logger.Error("panic recovered", "panic", rvr, "stack", string(debug.Stack()))
					pd := ProblemDetail{
						Type:     "about:blank",
						Title:    "Internal Server Error",
						Status:   500,
						Detail:   "an unexpected error occurred",
						Instance: r.URL.Path,
					}
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(500)
					json.NewEncoder(w).Encode(pd)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
