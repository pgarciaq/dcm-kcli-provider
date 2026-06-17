package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

type reqIDKey struct{}

// RequestLogger stores the chi request ID in context for later use.
// No per-request slog.Logger allocation; callers use LoggerFromContext
// which lazily adds the request_id attribute.
func RequestLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := middleware.GetReqID(r.Context())
			if reqID != "" {
				ctx := context.WithValue(r.Context(), reqIDKey{}, reqID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LoggerFromContext returns a logger enriched with the request_id from context.
// The child logger is created on demand, not per-request.
func LoggerFromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	if id, ok := ctx.Value(reqIDKey{}).(string); ok && id != "" {
		return base.With("request_id", id)
	}
	return base
}

// RequestIDFromContext returns the request ID string if present.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(reqIDKey{}).(string); ok {
		return id
	}
	return ""
}
