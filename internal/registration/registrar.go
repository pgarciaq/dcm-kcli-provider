package registration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	spmv1alpha1 "github.com/dcm-project/service-provider-manager/api/v1alpha1/provider"
	spmclient "github.com/dcm-project/service-provider-manager/pkg/client/provider"
	"github.com/google/uuid"
)

var errNonRetryable = errors.New("non-retryable")

type ProviderConfig struct {
	ID            string
	Name          string
	Endpoint      string
	ServiceType   string
	SchemaVersion string
}

type Option func(*Registrar)

func SetInitialBackoff(d time.Duration) Option {
	return func(r *Registrar) { r.initialBackoff = d }
}

func SetMaxBackoff(d time.Duration) Option {
	return func(r *Registrar) { r.maxBackoff = d }
}

type Registrar struct {
	client         *spmclient.ClientWithResponses
	providerCfg    ProviderConfig
	logger         *slog.Logger
	initialBackoff time.Duration
	maxBackoff     time.Duration

	startOnce sync.Once
	done      chan struct{}
}

func NewRegistrar(spmURL string, providerCfg ProviderConfig, logger *slog.Logger, opts ...Option) (*Registrar, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	client, err := spmclient.NewClientWithResponses(
		spmURL,
		spmclient.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("creating SPM client: %w", err)
	}

	r := &Registrar{
		client:         client,
		providerCfg:    providerCfg,
		logger:         logger,
		initialBackoff: 1 * time.Second,
		maxBackoff:     60 * time.Second,
		done:           make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

func (r *Registrar) StartBackground(ctx context.Context) {
	r.startOnce.Do(func() {
		go func() {
			defer close(r.done)
			r.run(ctx)
		}()
	})
}

func (r *Registrar) Done() <-chan struct{} {
	return r.done
}

func (r *Registrar) run(ctx context.Context) {
	backoff := r.initialBackoff

	for {
		if err := r.register(ctx); err == nil {
			return
		} else if errors.Is(err, errNonRetryable) {
			r.logger.Error("registration failed with non-retryable error, giving up", "error", err)
			return
		} else {
			r.logger.Warn("registration failed, will retry", "error", err)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}

		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}
}

func (r *Registrar) register(ctx context.Context) error {
	providerUUID, err := uuid.Parse(r.providerCfg.ID)
	if err != nil {
		return fmt.Errorf("invalid provider ID %q: %v: %w", r.providerCfg.ID, err, errNonRetryable)
	}

	providerID := providerUUID.String()
	params := &spmv1alpha1.CreateProviderParams{Id: &providerID}

	provider := spmv1alpha1.Provider{
		Name:          r.providerCfg.Name,
		Endpoint:      r.providerCfg.Endpoint,
		ServiceType:   r.providerCfg.ServiceType,
		SchemaVersion: r.providerCfg.SchemaVersion,
	}

	resp, err := r.client.CreateProviderWithResponse(ctx, params, provider)
	if err != nil {
		return fmt.Errorf("failed to register provider: %w", err)
	}

	switch resp.StatusCode() {
	case http.StatusCreated:
		r.logger.Info("registered new provider", "name", r.providerCfg.Name, "id", *resp.JSON201.Id)
	case http.StatusOK:
		r.logger.Info("updated existing provider", "name", r.providerCfg.Name, "id", *resp.JSON200.Id)
	case http.StatusConflict:
		return fmt.Errorf("conflict registering provider: %s: %w", resp.ApplicationproblemJSON409.Title, errNonRetryable)
	case http.StatusBadRequest:
		return fmt.Errorf("validation error: %s: %w", resp.ApplicationproblemJSON400.Title, errNonRetryable)
	default:
		sc := resp.StatusCode()
		if sc >= 400 && sc < 500 {
			return fmt.Errorf("registration returned non-retryable status %d: %w", sc, errNonRetryable)
		}
		return fmt.Errorf("unexpected response status: %d", sc)
	}

	return nil
}
