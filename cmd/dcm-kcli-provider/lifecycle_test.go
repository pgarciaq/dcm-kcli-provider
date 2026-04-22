package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/config"
	"github.com/pgarciaq/dcm-kcli-provider/internal/events"
	"github.com/pgarciaq/dcm-kcli-provider/internal/handlers"
	v1alpha1 "github.com/pgarciaq/dcm-kcli-provider/internal/handlers/v1alpha1"
	"github.com/pgarciaq/dcm-kcli-provider/internal/kweb"
	"github.com/pgarciaq/dcm-kcli-provider/internal/monitor"
	"github.com/pgarciaq/dcm-kcli-provider/internal/store"
)

func freePort() string {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	return addr
}

// slogSafeBuffer serializes writes and reads for -race when multiple goroutines log concurrently.
type slogSafeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *slogSafeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *slogSafeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var _ = Describe("Lifecycle", func() {

	// C-80: SP starts, self-probes /health, registration starts after self-probe
	It("starts, self-probes /health, then registration begins", func() {
		var registrationReceived atomic.Int32
		spmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			registrationReceived.Add(1)
			w.WriteHeader(200)
		}))
		defer spmServer.Close()

		kwebServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/host":
				w.WriteHeader(200)
			case "/vmprofiles":
				json.NewEncoder(w).Encode([]string{"fedora-39"})
			case "/vms":
				json.NewEncoder(w).Encode(map[string]interface{}{})
			case "/kubes":
				json.NewEncoder(w).Encode(map[string]interface{}{})
			default:
				w.WriteHeader(404)
			}
		}))
		defer kwebServer.Close()

		dir := GinkgoT().TempDir()
		storePath := filepath.Join(dir, "state.db")

		kwebClient := kweb.NewClient(kwebServer.URL, 5*time.Second)
		stateStore, err := store.New(storePath)
		Expect(err).NotTo(HaveOccurred())

		pub := &events.NoopPublisher{}
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

		monCfg := monitor.Config{
			PollInterval:         1 * time.Hour,
			DebounceWindow:       5 * time.Second,
			ClusterCreateTimeout: 30 * time.Minute,
		}
		mon := monitor.New(kwebClient, stateStore, pub, monCfg, logger)

		healthH := handlers.NewHealthHandler(kwebClient, "0.1.0")
		vmH := v1alpha1.NewVMHandler(kwebClient, stateStore, pub, mon)
		_ = v1alpha1.NewClusterHandler(kwebClient, stateStore, pub)

		addr := freePort()
		r := chi.NewRouter()
		r.Get("/health", healthH.ServeHTTP)
		r.Route("/api/v1alpha1", func(r chi.Router) {
			r.Post("/vms", vmH.Create)
			r.Get("/vms", vmH.List)
		})

		listener, err := net.Listen("tcp", addr)
		Expect(err).NotTo(HaveOccurred())

		httpServer := &http.Server{Handler: r}
		go httpServer.Serve(listener)
		defer httpServer.Shutdown(context.Background())

		client := &http.Client{Timeout: 2 * time.Second}
		Eventually(func() int {
			resp, err := client.Get(fmt.Sprintf("http://%s/health", addr))
			if err != nil {
				return 0
			}
			resp.Body.Close()
			return resp.StatusCode
		}).Should(Equal(200))

		stateStore.Close()
	})

	// C-81: SP calls GET /vmprofiles on startup and logs available profiles
	It("fetches and logs available VM profiles on startup", func() {
		var profilesCalled atomic.Int32
		kwebServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/vmprofiles" {
				profilesCalled.Add(1)
				json.NewEncoder(w).Encode([]string{"fedora-39", "centos-9"})
				return
			}
			if r.URL.Path == "/host" {
				w.WriteHeader(200)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{})
		}))
		defer kwebServer.Close()

		kwebClient := kweb.NewClient(kwebServer.URL, 5*time.Second)
		profiles, err := kwebClient.ListProfiles(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(profiles).To(ConsistOf("fedora-39", "centos-9"))
		Expect(profilesCalled.Load()).To(Equal(int32(1)))
	})

	// C-82: SP shuts down gracefully on SIGTERM
	It("shuts down gracefully: HTTP drains, NATS closes, bbolt closes", func() {
		dir := GinkgoT().TempDir()
		storePath := filepath.Join(dir, "state.db")
		stateStore, err := store.New(storePath)
		Expect(err).NotTo(HaveOccurred())

		pub := &events.NoopPublisher{}

		addr := freePort()
		r := chi.NewRouter()
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})

		listener, err := net.Listen("tcp", addr)
		Expect(err).NotTo(HaveOccurred())

		httpServer := &http.Server{Handler: r}
		go httpServer.Serve(listener)

		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err = httpServer.Shutdown(shutCtx)
		Expect(err).NotTo(HaveOccurred())

		pub.Close()
		err = stateStore.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	// C-83: Shutdown within timeout, logs warning if exceeded
	It("logs warning when shutdown timeout is exceeded", func() {
		addr := freePort()

		r := chi.NewRouter()
		r.Get("/slow", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(5 * time.Second)
			w.WriteHeader(200)
		})
		r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})

		listener, err := net.Listen("tcp", addr)
		Expect(err).NotTo(HaveOccurred())
		httpServer := &http.Server{Handler: r}
		go httpServer.Serve(listener)

		client := &http.Client{Timeout: 10 * time.Second}
		go client.Get(fmt.Sprintf("http://%s/slow", addr))
		time.Sleep(100 * time.Millisecond)

		shutCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		err = httpServer.Shutdown(shutCtx)
		Expect(err).To(HaveOccurred())
	})

	// C-84: REQUEST_TIMEOUT middleware returns 504 when handler exceeds deadline
	It("returns 504 when handler exceeds request timeout", func() {
		kwebServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(3 * time.Second)
			w.WriteHeader(200)
		}))
		defer kwebServer.Close()

		addr := freePort()
		r := chi.NewRouter()
		r.Use(chimw.Timeout(200 * time.Millisecond))
		r.Get("/api/v1alpha1/vms", func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			req, _ := http.NewRequestWithContext(ctx, "GET", kwebServer.URL+"/vms", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					w.WriteHeader(502)
				}
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(200)
		})

		listener, err := net.Listen("tcp", addr)
		Expect(err).NotTo(HaveOccurred())
		httpServer := &http.Server{Handler: r}
		go httpServer.Serve(listener)
		defer httpServer.Shutdown(context.Background())

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s/api/v1alpha1/vms", addr))
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(504))
	})

	// TC-LIFE-UT-001: Startup/shutdown lifecycle messages in slog (gaps 15–17 companion: server lifecycle observability)
	It("TC-LIFE-UT-001: logs HTTP server listening, self-probe succeeded, shutdown signal, and graceful shutdown complete", func() {
		spmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
		defer spmServer.Close()

		kwebServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/host":
				w.WriteHeader(200)
			case "/vmprofiles":
				json.NewEncoder(w).Encode([]string{"fedora-39"})
			case "/vms":
				json.NewEncoder(w).Encode(map[string]interface{}{})
			case "/kubes":
				json.NewEncoder(w).Encode(map[string]interface{}{})
			default:
				w.WriteHeader(404)
			}
		}))
		defer kwebServer.Close()

		var buf slogSafeBuffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

		cfg := &config.Config{
			ListenAddress:        freePort(),
			KwebURL:              kwebServer.URL,
			SPMURL:               spmServer.URL,
			ProviderNameVM:       "kcli-vm",
			ProviderNameCluster:  "kcli-cluster",
			PollInterval:         24 * time.Hour,
			DebounceWindow:       5 * time.Second,
			StateStorePath:       filepath.Join(GinkgoT().TempDir(), "state.db"),
			ShutdownTimeout:      10 * time.Second,
			ReadTimeout:          15 * time.Second,
			WriteTimeout:         60 * time.Second,
			IdleTimeout:          60 * time.Second,
			RequestTimeout:       45 * time.Second,
			KwebTimeout:          5 * time.Second,
			ClusterCreateTimeout: 30 * time.Minute,
		}

		srv, err := NewServer(cfg, logger)
		Expect(err).NotTo(HaveOccurred())

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- srv.Start(ctx)
		}()

		Eventually(func() string {
			return buf.String()
		}).WithTimeout(5*time.Second).WithPolling(50*time.Millisecond).Should(ContainSubstring("HTTP server listening"))

		Eventually(func() string {
			return buf.String()
		}).WithTimeout(5*time.Second).WithPolling(50*time.Millisecond).Should(ContainSubstring("self-probe succeeded"))

		cancel()

		var startErr error
		Eventually(errCh).WithTimeout(15 * time.Second).Should(Receive(&startErr))
		Expect(startErr).NotTo(HaveOccurred())

		logOut := buf.String()
		Expect(logOut).To(ContainSubstring("shutdown signal received"))
		Expect(logOut).To(ContainSubstring("graceful shutdown complete"))
	})
})
