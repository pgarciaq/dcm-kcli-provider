package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"
)

type RegistrationRequest struct {
	Name        string   `json:"name"`
	ServiceType string   `json:"serviceType"`
	DisplayName string   `json:"displayName"`
	Endpoint    string   `json:"endpoint"`
	Metadata    Metadata `json:"metadata"`
	Operations  []string `json:"operations"`
}

type Metadata struct {
	Region string `json:"region,omitempty"`
	Zone   string `json:"zone,omitempty"`
}

type Registrar struct {
	spmURL     string
	httpClient *http.Client
	logger     *slog.Logger

	startOnce sync.Once
	done      chan struct{}
}

func NewRegistrar(spmURL string, logger *slog.Logger) *Registrar {
	return &Registrar{
		spmURL: spmURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

func (r *Registrar) StartBackground(ctx context.Context, req RegistrationRequest) {
	r.startOnce.Do(func() {
		r.done = make(chan struct{})
		go func() {
			defer close(r.done)
			if err := r.Register(ctx, req); err != nil {
				r.logger.Warn("background registration failed", "error", err)
			}
		}()
	})
}

func (r *Registrar) Done() <-chan struct{} {
	return r.done
}

func (r *Registrar) Register(ctx context.Context, req RegistrationRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling registration request: %w", err)
	}

	maxRetries := 10
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.spmURL+"/providers", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := r.httpClient.Do(httpReq)
		if err != nil {
			r.logger.Warn("registration request failed", "attempt", attempt, "error", err)
			r.backoff(ctx, attempt)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			r.logger.Info("registration successful", "name", req.Name, "serviceType", req.ServiceType)
			return nil
		}

		if resp.StatusCode >= 500 {
			r.logger.Warn("registration received server error", "attempt", attempt, "status", resp.StatusCode)
			r.backoff(ctx, attempt)
			continue
		}

		return fmt.Errorf("registration failed with status %d", resp.StatusCode)
	}

	return fmt.Errorf("registration failed after %d retries", maxRetries)
}

func (r *Registrar) backoff(ctx context.Context, attempt int) {
	delay := time.Duration(math.Pow(2, float64(attempt))) * 100 * time.Millisecond
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}
