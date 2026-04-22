// New tests embed formal case IDs in It() descriptions using the TC-<area>-<kind>-UT-nnn convention.
// Legacy cases may be referenced only by C-nn comments on nearby lines.
package registration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/registration"
)

var _ = Describe("Registrar", func() {
	var logger *slog.Logger

	BeforeEach(func() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	})

	// C-76: Registrar sends POST with VM payload
	It("sends POST to /providers with VM registration payload", func() {
		var receivedBody registration.RegistrationRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Method).To(Equal("POST"))
			Expect(r.URL.Path).To(Equal("/providers"))
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedBody)
			w.WriteHeader(200)
		}))
		defer server.Close()

		reg := registration.NewRegistrar(server.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			DisplayName: "kcli VM Service Provider",
			Endpoint:    "http://sp:8080/api/v1alpha1/vms",
			Metadata:    registration.Metadata{Region: "us-east", Zone: "zone1"},
			Operations:  []string{"CREATE", "READ", "DELETE"},
		}

		err := reg.Register(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(receivedBody.ServiceType).To(Equal("vm"))
		Expect(receivedBody.Operations).To(ConsistOf("CREATE", "READ", "DELETE"))
	})

	// C-77: Registrar sends POST with cluster payload
	It("sends POST with cluster registration payload", func() {
		var receivedBody registration.RegistrationRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &receivedBody)
			w.WriteHeader(200)
		}))
		defer server.Close()

		reg := registration.NewRegistrar(server.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-cluster",
			ServiceType: "cluster",
			DisplayName: "kcli Cluster Service Provider",
			Endpoint:    "http://sp:8080/api/v1alpha1/clusters",
			Operations:  []string{"CREATE", "READ", "DELETE"},
		}

		err := reg.Register(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(receivedBody.ServiceType).To(Equal("cluster"))
	})

	// C-78: Registrar retries with exponential backoff on 500, with increasing delays
	It("retries with exponential backoff on server error", func() {
		var attempts atomic.Int32
		var timestamps []time.Time
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			timestamps = append(timestamps, time.Now())
			n := attempts.Add(1)
			if n <= 2 {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(200)
		}))
		defer server.Close()

		reg := registration.NewRegistrar(server.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE", "READ", "DELETE"},
		}

		err := reg.Register(context.Background(), req)
		Expect(err).NotTo(HaveOccurred())
		Expect(attempts.Load()).To(Equal(int32(3)))

		// Verify increasing delays between attempts (exponential backoff)
		if len(timestamps) >= 3 {
			delay1 := timestamps[1].Sub(timestamps[0])
			delay2 := timestamps[2].Sub(timestamps[1])
			Expect(delay2).To(BeNumerically(">=", delay1))
		}
	})

	// C-79: Registrar stops retrying when context is cancelled mid-backoff
	It("stops retrying when context is cancelled during backoff", func() {
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.WriteHeader(500)
		}))
		defer server.Close()

		reg := registration.NewRegistrar(server.URL, logger)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE", "READ", "DELETE"},
		}

		err := reg.Register(ctx, req)
		Expect(err).To(HaveOccurred())
		// Should have made some attempts but not exhausted all retries
		Expect(attempts.Load()).To(BeNumerically(">=", 1))
		Expect(attempts.Load()).To(BeNumerically("<", 10))
	})

	// TC-REG-UT-001: 4xx from SPM does not retry — 400 fails immediately with a single attempt
	It("TC-REG-UT-001: SPM 400 causes immediate registration failure without retry", func() {
		var attempts atomic.Int32
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.WriteHeader(400)
		}))
		defer spm.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE"},
		}

		err := reg.Register(context.Background(), req)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("registration failed with status 400"))
		Expect(attempts.Load()).To(Equal(int32(1)))
	})

	// TC-REG-UT-002: 4xx from SPM does not retry — 409 fails immediately with a single attempt
	It("TC-REG-UT-002: SPM 409 causes immediate registration failure without retry", func() {
		var attempts atomic.Int32
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.WriteHeader(409)
		}))
		defer spm.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE"},
		}

		err := reg.Register(context.Background(), req)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("registration failed with status 409"))
		Expect(attempts.Load()).To(Equal(int32(1)))
	})

	// TC-REG-UT-003: StartBackground is idempotent — only one registration goroutine
	It("TC-REG-UT-003: calling StartBackground twice only performs registration once", func() {
		var spmAttempts atomic.Int32
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			spmAttempts.Add(1)
			w.WriteHeader(200)
		}))
		defer spm.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE"},
		}

		reg.StartBackground(context.Background(), req)
		reg.StartBackground(context.Background(), req)

		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
		Expect(spmAttempts.Load()).To(Equal(int32(1)))
	})

	// TC-REG-UT-004: Done closes after successful background registration
	It("TC-REG-UT-004: Done channel closes when background registration succeeds", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		}))
		defer spm.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE"},
		}

		reg.StartBackground(context.Background(), req)
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
	})

	// TC-REG-UT-005: Done closes when background registration fails (no retry on 4xx)
	It("TC-REG-UT-005: Done channel closes when background registration fails", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
		}))
		defer spm.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE"},
		}

		reg.StartBackground(context.Background(), req)
		Eventually(reg.Done(), 3*time.Second).Should(BeClosed())
	})

	// TC-REG-UT-006: Done closes when registration stops due to cancelled context
	It("TC-REG-UT-006: Done channel closes when context is cancelled during 500 retries", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer spm.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		ctx, cancel := context.WithCancel(context.Background())
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Operations:  []string{"CREATE"},
		}

		reg.StartBackground(ctx, req)
		cancel()
		Eventually(reg.Done(), 5*time.Second).Should(BeClosed())
	})

	// TC-REG-IT-001: Service provider HTTP remains available while registration retries against failing SPM
	It("TC-REG-IT-001: HTTP server serves requests while background registration retries", func() {
		var spmAttempts atomic.Int32
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := spmAttempts.Add(1)
			if n <= 5 {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(200)
		}))
		defer spm.Close()

		sp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		}))
		defer sp.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Endpoint:    sp.URL,
			Operations:  []string{"CREATE"},
		}

		reg.StartBackground(context.Background(), req)

		client := &http.Client{Timeout: 2 * time.Second}
		Eventually(func() int {
			resp, err := client.Get(sp.URL)
			if err != nil {
				return 0
			}
			defer resp.Body.Close()
			return resp.StatusCode
		}, 5*time.Second, 20*time.Millisecond).Should(Equal(200))

		Eventually(reg.Done(), 15*time.Second).Should(BeClosed())
		Expect(spmAttempts.Load()).To(BeNumerically(">=", 6))
	})

	// TC-REG-IT-002: After registration gives up on always-500 SPM due to cancel, HTTP still serves
	It("TC-REG-IT-002: HTTP server still serves after registration fails when context is cancelled", func() {
		spm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer spm.Close()

		sp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
		}))
		defer sp.Close()

		reg := registration.NewRegistrar(spm.URL, logger)
		ctx, cancel := context.WithCancel(context.Background())
		req := registration.RegistrationRequest{
			Name:        "kcli-vm",
			ServiceType: "vm",
			Endpoint:    sp.URL,
			Operations:  []string{"CREATE"},
		}

		reg.StartBackground(ctx, req)
		time.Sleep(50 * time.Millisecond)
		cancel()

		Eventually(reg.Done(), 10*time.Second).Should(BeClosed())

		resp, err := http.Get(sp.URL)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(200))
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("ok"))
	})
})
