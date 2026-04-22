package registration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	spmv1alpha1 "github.com/dcm-project/service-provider-manager/api/v1alpha1/provider"
	"github.com/google/uuid"

	"github.com/pgarciaq/dcm-kcli-provider/internal/registration"
)

var _ = Describe("Registrar", func() {
	var (
		logger      *slog.Logger
		providerCfg registration.ProviderConfig
		validUUID   string
	)

	BeforeEach(func() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		validUUID = uuid.New().String()
		providerCfg = registration.ProviderConfig{
			ID:            validUUID,
			Name:          "kcli-vm",
			Endpoint:      "http://sp:8080/api/v1alpha1",
			ServiceType:   "vm",
			SchemaVersion: "v1alpha1",
		}
	})

	// C-76: Registration sends POST /providers with correct payload using SPM client
	It("sends POST to /providers with snake_case fields and schema_version", func() {
		var receivedBody spmv1alpha1.Provider
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			Expect(req.Method).To(Equal("POST"))
			Expect(req.URL.Path).To(Equal("/providers"))
			Expect(req.URL.Query().Get("id")).To(Equal(validUUID))
			json.NewDecoder(req.Body).Decode(&receivedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(spmv1alpha1.Provider{Id: &validUUID, Name: "kcli-vm"})
		}))
		defer server.Close()

		reg, err := registration.NewRegistrar(server.URL, providerCfg, logger)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())

		Expect(receivedBody.Name).To(Equal("kcli-vm"))
		Expect(receivedBody.ServiceType).To(Equal("vm"))
		Expect(receivedBody.SchemaVersion).To(Equal("v1alpha1"))
		Expect(receivedBody.Endpoint).To(Equal("http://sp:8080/api/v1alpha1"))
	})

	// C-77: Registration works for cluster service type too
	It("registers cluster service type successfully", func() {
		var receivedBody spmv1alpha1.Provider
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			json.NewDecoder(req.Body).Decode(&receivedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(spmv1alpha1.Provider{Id: &validUUID, Name: "kcli-cluster"})
		}))
		defer server.Close()

		clCfg := registration.ProviderConfig{
			ID:            validUUID,
			Name:          "kcli-cluster",
			Endpoint:      "http://sp:8080/api/v1alpha1",
			ServiceType:   "cluster",
			SchemaVersion: "v1alpha1",
		}
		reg, err := registration.NewRegistrar(server.URL, clCfg, logger)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
		Expect(receivedBody.ServiceType).To(Equal("cluster"))
	})

	// C-78: Retries with exponential backoff on 500
	It("retries with exponential backoff on server error", func() {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := attempts.Add(1)
			if n <= 2 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(spmv1alpha1.Provider{Id: &validUUID, Name: "kcli-vm"})
		}))
		defer server.Close()

		reg, err := registration.NewRegistrar(server.URL, providerCfg, logger,
			registration.SetInitialBackoff(10*time.Millisecond),
			registration.SetMaxBackoff(50*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 5*time.Second).Should(BeClosed())
		Expect(attempts.Load()).To(BeNumerically(">=", int32(3)))
	})

	// C-79: Stops retrying when context is cancelled
	It("stops retrying when context is cancelled during backoff", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		reg, err := registration.NewRegistrar(server.URL, providerCfg, logger,
			registration.SetInitialBackoff(10*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())

		ctx, cancel := context.WithCancel(context.Background())
		reg.StartBackground(ctx)
		time.Sleep(50 * time.Millisecond)
		cancel()
		Eventually(reg.Done(), 5*time.Second).Should(BeClosed())
	})

	// TC-REG-UT-001: 400 from SPM is non-retryable
	It("TC-REG-UT-001: SPM 400 causes immediate failure without retry", func() {
		var attempts atomic.Int32
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(spmv1alpha1.Error{Title: "Invalid config", Type: "about:blank"})
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger,
			registration.SetInitialBackoff(10*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
		Expect(attempts.Load()).To(Equal(int32(1)))
	})

	// TC-REG-UT-002: 409 from SPM is non-retryable
	It("TC-REG-UT-002: SPM 409 causes immediate failure without retry", func() {
		var attempts atomic.Int32
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(spmv1alpha1.Error{Title: "Conflict", Type: "about:blank"})
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger,
			registration.SetInitialBackoff(10*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
		Expect(attempts.Load()).To(Equal(int32(1)))
	})

	// TC-REG-UT-003: StartBackground is idempotent
	It("TC-REG-UT-003: calling StartBackground twice only registers once", func() {
		var attempts atomic.Int32
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(spmv1alpha1.Provider{Id: &validUUID, Name: "kcli-vm"})
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger)
		Expect(err).NotTo(HaveOccurred())

		ctx := context.Background()
		reg.StartBackground(ctx)
		reg.StartBackground(ctx)

		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
		Expect(attempts.Load()).To(Equal(int32(1)))
	})

	// TC-REG-UT-004: Done closes after success
	It("TC-REG-UT-004: Done channel closes when registration succeeds", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(spmv1alpha1.Provider{Id: &validUUID, Name: "kcli-vm"})
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
	})

	// TC-REG-UT-005: Done closes on non-retryable failure
	It("TC-REG-UT-005: Done channel closes when registration hits non-retryable error", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(spmv1alpha1.Error{Title: "Bad request", Type: "about:blank"})
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
	})

	// TC-REG-UT-006: Done closes on context cancellation during retries
	It("TC-REG-UT-006: Done channel closes when context is cancelled during 500 retries", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger,
			registration.SetInitialBackoff(10*time.Millisecond),
		)
		Expect(err).NotTo(HaveOccurred())

		ctx, cancel := context.WithCancel(context.Background())
		reg.StartBackground(ctx)
		time.Sleep(30 * time.Millisecond)
		cancel()
		Eventually(reg.Done(), 5*time.Second).Should(BeClosed())
	})

	// Invalid UUID is non-retryable
	It("fails immediately with invalid provider UUID", func() {
		providerCfg.ID = "not-a-uuid"
		spm := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			Fail("should not reach SPM with invalid UUID")
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
	})

	// 200 (update existing) is treated as success
	It("treats 200 response as successful update", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(spmv1alpha1.Provider{Id: &validUUID, Name: "kcli-vm"})
		}))
		defer spm.Close()

		reg, err := registration.NewRegistrar(spm.URL, providerCfg, logger)
		Expect(err).NotTo(HaveOccurred())

		reg.StartBackground(context.Background())
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
	})
})
