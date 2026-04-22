package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type HealthChecker interface {
	CheckHealth(ctx context.Context) (bool, error)
}

type HealthHandler struct {
	checker   HealthChecker
	version   string
	startedAt time.Time
}

func NewHealthHandler(checker HealthChecker, version string) *HealthHandler {
	return &HealthHandler{
		checker:   checker,
		version:   version,
		startedAt: time.Now(),
	}
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Uptime  int64  `json:"uptime"`
	Message string `json:"message,omitempty"`
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	uptime := int64(time.Since(h.startedAt).Seconds())

	healthy, err := h.checker.CheckHealth(r.Context())
	if err != nil || !healthy {
		msg := "kweb unreachable"
		if err != nil {
			msg = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(healthResponse{
			Status:  "fail",
			Version: h.version,
			Uptime:  uptime,
			Message: msg,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(healthResponse{
		Status:  "pass",
		Version: h.version,
		Uptime:  uptime,
	})
}
