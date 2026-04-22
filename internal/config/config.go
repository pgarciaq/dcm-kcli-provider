package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	ListenAddress        string
	KwebURL              string
	SPMURL               string
	NATSURL              string
	ProviderIDVM         string
	ProviderIDCluster    string
	ProviderNameVM       string
	ProviderNameCluster  string
	SchemaVersion        string
	Region               string
	Zone                 string
	PollInterval         time.Duration
	DebounceWindow       time.Duration
	StateStorePath       string
	LogLevel             string
	ShutdownTimeout      time.Duration
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	RequestTimeout       time.Duration
	KwebTimeout          time.Duration
	ClusterCreateTimeout time.Duration
}

func Load() (*Config, error) {
	kwebURL := os.Getenv("KWEB_URL")
	if kwebURL == "" {
		return nil, fmt.Errorf("KWEB_URL is required")
	}

	spmURL := os.Getenv("SPM_URL")
	if spmURL == "" {
		return nil, fmt.Errorf("SPM_URL is required")
	}

	cfg := &Config{
		ListenAddress:       envOrDefault("LISTEN_ADDRESS", ":8080"),
		KwebURL:             kwebURL,
		SPMURL:              spmURL,
		NATSURL:             os.Getenv("NATS_URL"),
		ProviderIDVM:        envOrDefault("PROVIDER_ID_VM", ""),
		ProviderIDCluster:   envOrDefault("PROVIDER_ID_CLUSTER", ""),
		ProviderNameVM:      envOrDefault("PROVIDER_NAME_VM", "kcli-vm"),
		ProviderNameCluster: envOrDefault("PROVIDER_NAME_CLUSTER", "kcli-cluster"),
		SchemaVersion:       envOrDefault("SCHEMA_VERSION", "v1alpha1"),
		Region:              os.Getenv("REGION"),
		Zone:                os.Getenv("ZONE"),
		StateStorePath:      envOrDefault("STATE_STORE_PATH", "/data/state.db"),
		LogLevel:            envOrDefault("LOG_LEVEL", "info"),
	}

	var err error
	if cfg.PollInterval, err = parseDuration("POLL_INTERVAL", "30s"); err != nil {
		return nil, err
	}
	if cfg.DebounceWindow, err = parseDuration("DEBOUNCE_WINDOW", "5s"); err != nil {
		return nil, err
	}
	if cfg.ShutdownTimeout, err = parseDuration("SHUTDOWN_TIMEOUT", "10s"); err != nil {
		return nil, err
	}
	if cfg.ReadTimeout, err = parseDuration("READ_TIMEOUT", "15s"); err != nil {
		return nil, err
	}
	if cfg.WriteTimeout, err = parseDuration("WRITE_TIMEOUT", "60s"); err != nil {
		return nil, err
	}
	if cfg.IdleTimeout, err = parseDuration("IDLE_TIMEOUT", "60s"); err != nil {
		return nil, err
	}
	if cfg.RequestTimeout, err = parseDuration("REQUEST_TIMEOUT", "45s"); err != nil {
		return nil, err
	}
	if cfg.KwebTimeout, err = parseDuration("KWEB_TIMEOUT", "120s"); err != nil {
		return nil, err
	}
	if cfg.ClusterCreateTimeout, err = parseDuration("CLUSTER_CREATE_TIMEOUT", "30m"); err != nil {
		return nil, err
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(envKey, fallback string) (time.Duration, error) {
	raw := envOrDefault(envKey, fallback)
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %q: %w", envKey, raw, err)
	}
	return d, nil
}
