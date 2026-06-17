// Package metrics defines Prometheus metrics for the dcm-kcli-provider.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "dcm_kcli",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests by method, path, and status code.",
	}, []string{"method", "route", "code"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "dcm_kcli",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "route"})

	ResourcesManaged = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "dcm_kcli",
		Name:      "resources_managed",
		Help:      "Number of resources currently tracked by the provider.",
	}, []string{"type"})

	KwebRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "dcm_kcli",
		Name:      "kweb_requests_total",
		Help:      "Total upstream kweb API calls by operation and result.",
	}, []string{"operation", "result"})

	NATSEventsPublished = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "dcm_kcli",
		Name:      "nats_events_published_total",
		Help:      "Total NATS CloudEvents published by type.",
	}, []string{"event_type"})

	RegistrationStatus = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "dcm_kcli",
		Name:      "registration_status",
		Help:      "Provider registration status (1=registered, 0=retrying).",
	}, []string{"provider"})

	RegistrationAttemptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "dcm_kcli",
		Name:      "registration_attempts_total",
		Help:      "Total SPM registration attempts by provider and result.",
	}, []string{"provider", "result"})

	MonitorPollDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "dcm_kcli",
		Name:      "monitor_poll_duration_seconds",
		Help:      "Duration of each monitor poll cycle.",
		Buckets:   prometheus.DefBuckets,
	})

	MonitorStatusChanges = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "dcm_kcli",
		Name:      "monitor_status_changes_total",
		Help:      "Total status change events scheduled by the monitor, including coalesced debounces.",
	})
)

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Middleware records HTTP request count and duration using chi's route pattern
// to avoid label cardinality issues from dynamic path segments.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = r.URL.Path
		}
		method := r.Method
		code := strconv.Itoa(sw.code)

		RequestsTotal.WithLabelValues(method, route, code).Inc()
		RequestDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	})
}

// RecordKweb records a kweb API call outcome.
func RecordKweb(operation string, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	KwebRequestsTotal.WithLabelValues(operation, result).Inc()
}
