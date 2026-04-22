package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	apiv1alpha1 "github.com/pgarciaq/dcm-kcli-provider/api/v1alpha1"
	apiserver "github.com/pgarciaq/dcm-kcli-provider/internal/api/server"
	"github.com/pgarciaq/dcm-kcli-provider/internal/config"
	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/monitor"
	"github.com/pgarciaq/dcm-kcli-provider/internal/registration"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

const version = "0.1.0"

type Server struct {
	cfg        *config.Config
	logger     *slog.Logger
	httpServer *http.Server
	store      *store.Store
	publisher  events.Publisher
	kwebClient *kweb.Client
	monitor    *monitor.Monitor
	registrar  *registration.Registrar
	listener   net.Listener
}

func NewServer(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	s := &Server{
		cfg:    cfg,
		logger: logger,
	}

	var err error
	s.store, err = store.New(cfg.StateStorePath)
	if err != nil {
		return nil, fmt.Errorf("opening state store: %w", err)
	}

	s.kwebClient = kweb.NewClient(cfg.KwebURL, cfg.KwebTimeout)

	if cfg.NATSURL != "" {
		s.publisher, err = events.NewNATSPublisher(cfg.NATSURL)
		if err != nil {
			s.logger.Warn("NATS connection failed, falling back to NoopPublisher", "error", err)
			s.publisher = &events.NoopPublisher{}
		}
	} else {
		s.publisher = &events.NoopPublisher{}
	}

	monCfg := monitor.Config{
		PollInterval:         cfg.PollInterval,
		DebounceWindow:       cfg.DebounceWindow,
		ClusterCreateTimeout: cfg.ClusterCreateTimeout,
	}
	s.monitor = monitor.New(s.kwebClient, s.store, s.publisher, monCfg, logger)
	vmProviderCfg := registration.ProviderConfig{
		ID:            cfg.ProviderIDVM,
		Name:          cfg.ProviderNameVM,
		Endpoint:      fmt.Sprintf("http://%s/api/v1alpha1", cfg.ListenAddress),
		ServiceType:   "vm",
		SchemaVersion: cfg.SchemaVersion,
	}
	if vmProviderCfg.ID == "" {
		vmProviderCfg.ID = uuid.New().String()
	}
	s.registrar, err = registration.NewRegistrar(cfg.SPMURL, vmProviderCfg, logger)
	if err != nil {
		s.store.Close()
		return nil, fmt.Errorf("creating registrar: %w", err)
	}

	impl := apiserver.NewStrictServerImpl(s.kwebClient, s.store, s.publisher, s.monitor, version)
	strictHandler := apiserver.NewStrictHandler(impl, nil)

	swagger, err := apiv1alpha1.GetSwagger()
	if err != nil {
		s.store.Close()
		return nil, fmt.Errorf("loading OpenAPI spec: %w", err)
	}

	baseURL := ""
	if len(swagger.Servers) > 0 {
		baseURL = swagger.Servers[0].URL
	}

	r := chi.NewRouter()
	r.Use(handlers.PanicRecovery(logger))
	r.Use(middleware.Logger)
	r.Use(middleware.Timeout(cfg.RequestTimeout))
	r.Use(nethttpmiddleware.OapiRequestValidatorWithOptions(swagger, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
		SilenceServersWarning: true,
	}))

	apiserver.HandlerFromMuxWithBaseURL(strictHandler, r, baseURL)

	s.httpServer = &http.Server{
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	var err error
	s.listener, err = net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.cfg.ListenAddress, err)
	}
	s.logger.Info("HTTP server listening", "address", s.listener.Addr().String())

	go s.httpServer.Serve(s.listener)

	if !s.selfProbe(ctx) {
		return fmt.Errorf("self-probe failed: /health did not return 200")
	}
	s.logger.Info("self-probe succeeded")

	profiles, err := s.kwebClient.ListProfiles(ctx)
	if err != nil {
		s.logger.Warn("failed to list kweb profiles on startup", "error", err)
	} else if len(profiles) == 0 {
		s.logger.Warn("no VM profiles configured in kweb")
	} else {
		s.logger.Info("available kweb VM profiles", "profiles", profiles)
	}

	var wg sync.WaitGroup

	monCtx, monCancel := context.WithCancel(ctx)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.monitor.Run(monCtx)
	}()

	s.registrar.StartBackground(ctx)

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-sigCtx.Done()
	s.logger.Info("shutdown signal received")

	s.shutdown(monCancel, &wg)
	return nil
}

func (s *Server) shutdown(monCancel context.CancelFunc, wg *sync.WaitGroup) {
	shutCtx, shutCancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer shutCancel()

	if err := s.httpServer.Shutdown(shutCtx); err != nil {
		s.logger.Warn("HTTP server shutdown error", "error", err)
	}

	monCancel()

	s.publisher.Close()
	if err := s.store.Close(); err != nil {
		s.logger.Warn("store close error", "error", err)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("graceful shutdown complete")
	case <-shutCtx.Done():
		s.logger.Warn("shutdown timeout exceeded, some goroutines may still be running")
	}
}

func (s *Server) selfProbe(ctx context.Context) bool {
	addr := s.listener.Addr().String()
	client := &http.Client{Timeout: 1 * time.Second}
	for i := 0; i < 30; i++ {
		if ctx.Err() != nil {
			return false
		}
		resp, err := client.Get(fmt.Sprintf("http://%s/api/v1alpha1/health", addr))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	srv, err := NewServer(cfg, logger)
	if err != nil {
		return err
	}

	return srv.Start(context.Background())
}
