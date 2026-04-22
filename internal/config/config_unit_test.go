// New tests embed formal case IDs in It() descriptions using the TC-<area>-<kind>-UT-nnn convention.
// Legacy cases may be referenced only by C-nn comments on nearby lines.
package config_test

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/pgarciaq/dcm-kcli-provider/internal/config"
)

var _ = Describe("Config", func() {
	var savedEnv map[string]string

	setMinimalEnv := func() {
		os.Setenv("KWEB_URL", "http://kweb:9000")
		os.Setenv("SPM_URL", "http://spm:8080")
	}

	BeforeEach(func() {
		savedEnv = map[string]string{}
		for _, key := range []string{
			"KWEB_URL", "SPM_URL", "LISTEN_ADDRESS", "NATS_URL",
			"PROVIDER_NAME_VM", "PROVIDER_NAME_CLUSTER", "REGION", "ZONE",
			"POLL_INTERVAL", "DEBOUNCE_WINDOW", "STATE_STORE_PATH", "LOG_LEVEL",
			"SHUTDOWN_TIMEOUT", "READ_TIMEOUT", "WRITE_TIMEOUT", "IDLE_TIMEOUT",
			"REQUEST_TIMEOUT", "KWEB_TIMEOUT", "CLUSTER_CREATE_TIMEOUT",
		} {
			savedEnv[key] = os.Getenv(key)
			os.Unsetenv(key)
		}
	})

	AfterEach(func() {
		for key, val := range savedEnv {
			if val == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, val)
			}
		}
	})

	// C-01: Load() returns error when KWEB_URL is missing
	It("returns error when KWEB_URL is missing", func() {
		os.Setenv("SPM_URL", "http://spm:8080")
		_, err := config.Load()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("KWEB_URL"))
	})

	// C-02: Load() returns error when SPM_URL is missing
	It("returns error when SPM_URL is missing", func() {
		os.Setenv("KWEB_URL", "http://kweb:9000")
		_, err := config.Load()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("SPM_URL"))
	})

	// C-03: Load() populates all defaults
	It("populates all defaults when only required vars are set", func() {
		setMinimalEnv()
		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())

		Expect(cfg.ListenAddress).To(Equal(":8080"))
		Expect(cfg.KwebURL).To(Equal("http://kweb:9000"))
		Expect(cfg.SPMURL).To(Equal("http://spm:8080"))
		Expect(cfg.PollInterval).To(Equal(30 * time.Second))
		Expect(cfg.DebounceWindow).To(Equal(5 * time.Second))
		Expect(cfg.StateStorePath).To(Equal("/data/state.db"))
		Expect(cfg.LogLevel).To(Equal("info"))
		Expect(cfg.ShutdownTimeout).To(Equal(10 * time.Second))
		Expect(cfg.ReadTimeout).To(Equal(15 * time.Second))
		Expect(cfg.WriteTimeout).To(Equal(60 * time.Second))
		Expect(cfg.IdleTimeout).To(Equal(60 * time.Second))
		Expect(cfg.RequestTimeout).To(Equal(45 * time.Second))
		Expect(cfg.KwebTimeout).To(Equal(120 * time.Second))
		Expect(cfg.ClusterCreateTimeout).To(Equal(30 * time.Minute))
	})

	// C-04: Load() parses duration strings
	It("parses custom duration strings", func() {
		setMinimalEnv()
		os.Setenv("POLL_INTERVAL", "10s")
		os.Setenv("DEBOUNCE_WINDOW", "2s")
		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.PollInterval).To(Equal(10 * time.Second))
		Expect(cfg.DebounceWindow).To(Equal(2 * time.Second))
	})

	// C-05: Load() returns error on invalid duration
	It("returns error on invalid duration string", func() {
		setMinimalEnv()
		os.Setenv("POLL_INTERVAL", "banana")
		_, err := config.Load()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("POLL_INTERVAL"))
		Expect(err.Error()).To(ContainSubstring("banana"))
	})

	// TC-CFG-UT-006: HTTP server timeout defaults from Load() are non-zero
	It("TC-CFG-UT-006: ReadTimeout, WriteTimeout, IdleTimeout defaults are non-zero", func() {
		setMinimalEnv()
		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ReadTimeout).To(BeNumerically(">", 0))
		Expect(cfg.WriteTimeout).To(BeNumerically(">", 0))
		Expect(cfg.IdleTimeout).To(BeNumerically(">", 0))
	})
})
