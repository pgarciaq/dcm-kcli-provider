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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/pgarciaq/dcm-kcli-provider/internal/config"
	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers/v1alpha1"
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
	s.registrar = registration.NewRegistrar(cfg.SPMURL, logger)

	healthH := handlers.NewHealthHandler(s.kwebClient, version)
	vmH := v1alpha1.NewVMHandler(s.kwebClient, s.store, s.publisher, s.monitor)
	clH := v1alpha1.NewClusterHandler(s.kwebClient, s.store, s.publisher)

	r := chi.NewRouter()
	// PanicRecovery returns RFC 7807 (application/problem+json) instead of Chi's default plain-text recoverer.
	r.Use(handlers.PanicRecovery(logger))
	// Request lifecycle (method, path, status, duration) is logged by Chi's middleware.Logger.
	r.Use(middleware.Logger)
	r.Use(middleware.Timeout(cfg.RequestTimeout))
	r.Get("/health", healthH.ServeHTTP)
	r.Route("/api/v1alpha1", func(r chi.Router) {
		r.Post("/vms", vmH.Create)
		r.Get("/vms", vmH.List)
		r.Get("/vms/{vmId}", vmH.Get)
		r.Delete("/vms/{vmId}", vmH.Delete)
		r.Post("/clusters", clH.Create)
		r.Get("/clusters", clH.List)
		r.Get("/clusters/{clusterId}", clH.Get)
		r.Delete("/clusters/{clusterId}", clH.Delete)
	})

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

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.registerVM(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.registerCluster(ctx)
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-sigCtx.Done()
	s.logger.Info("shutdown signal received")

	s.Shutdown(monCancel, &wg)
	return nil
}

func (s *Server) Shutdown(monCancel context.CancelFunc, wg *sync.WaitGroup) {
	shutCtx, shutCancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer shutCancel()

	if err := s.httpServer.Shutdown(shutCtx); err != nil {
		s.logger.Warn("HTTP server shutdown error", "error", err)
	}

	monCancel()

	s.publisher.Close()
	s.store.Close()

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
		resp, err := client.Get(fmt.Sprintf("http://%s/health", addr))
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

func (s *Server) registerVM(ctx context.Context) {
	req := registration.RegistrationRequest{
		Name:        s.cfg.ProviderNameVM,
		ServiceType: "vm",
		DisplayName: "kcli VM Service Provider",
		Endpoint:    fmt.Sprintf("http://%s/api/v1alpha1/vms", s.cfg.ListenAddress),
		Metadata: registration.Metadata{
			Region: s.cfg.Region,
			Zone:   s.cfg.Zone,
		},
		Operations: []string{"CREATE", "READ", "DELETE"},
	}
	if err := s.registrar.Register(ctx, req); err != nil {
		s.logger.Warn("VM registration failed", "error", err)
	}
}

func (s *Server) registerCluster(ctx context.Context) {
	req := registration.RegistrationRequest{
		Name:        s.cfg.ProviderNameCluster,
		ServiceType: "cluster",
		DisplayName: "kcli Cluster Service Provider",
		Endpoint:    fmt.Sprintf("http://%s/api/v1alpha1/clusters", s.cfg.ListenAddress),
		Metadata: registration.Metadata{
			Region: s.cfg.Region,
			Zone:   s.cfg.Zone,
		},
		Operations: []string{"CREATE", "READ", "DELETE"},
	}
	if err := s.registrar.Register(ctx, req); err != nil {
		s.logger.Warn("cluster registration failed", "error", err)
	}
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
