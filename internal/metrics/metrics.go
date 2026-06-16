// Package metrics defines Prometheus metrics for the dcm-kcli-provider.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "dcm_kcli",
		Name:      "http_requests_total",
		Help:      "Total HTTP requests by method, path, and status code.",
	}, []string{"method", "path", "code"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "dcm_kcli",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency in seconds.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path"})

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
)
