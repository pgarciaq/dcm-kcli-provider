# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Request correlation ID middleware (`X-Request-Id` header propagation) (AUD-01)
- Registration metrics: `dcm_kcli_registration_status`, `dcm_kcli_registration_attempts_total` (AUD-02)
- Monitor poll cycle metrics: `dcm_kcli_monitor_poll_duration_seconds`, `dcm_kcli_monitor_status_changes_total` (AUD-03)
- Readiness probe endpoint at `/ready` (OPS-01)
- Best-effort SPM deregistration on graceful shutdown (OPS-02)
- Request body size limit middleware (1 MB) (SEC-02)
- Provider hints allowlist for safe kweb parameter forwarding (SEC-03)
- `SECURITY.md` with vulnerability reporting guidance (GOV-02)
- `govulncheck` CI step for dependency vulnerability scanning (GOV-03)
- `CHANGELOG.md` following Keep a Changelog format (GOV-01)

### Changed

- List endpoints (`ListVMs`, `ListClusters`) now fetch store and kweb data in parallel (PERF-01)
- List results are sorted by `CreatedAt` descending for deterministic ordering (COR-02)
- Split `impl.go` (928 lines) into `vm_handlers.go`, `cluster_handlers.go`, `health_handlers.go`, `helpers.go` (DES-01)
- `startedAt` timestamp is shared from `Server` to `StrictServerImpl` via `WithStartedAt` option (DES-02)

### Security

- Documented trusted-network-only deployment requirement (SEC-01)
- Documented TLS limitation with reverse-proxy recommendation (SEC-04)
- Documented Docker Compose SSH key mount risk with mitigation advice (SEC-05)

### Documentation

- Added Security Considerations section to README
- Added Known Limitations section (pagination, VM creation concurrency, single-instance) to README
- Added `/ready` and `/health` endpoints to API table

## [0.2.0] - 2026-06-17

### Added

- Prometheus metrics endpoint at `/metrics`
- HTTP request metrics middleware (`http_requests_total`, `http_request_duration_seconds`)
- kweb client instrumentation (`dcm_kcli_kweb_requests_total`)
- NATS publish instrumentation (`dcm_kcli_nats_events_published_total`)
- Resource managed gauge (`dcm_kcli_resources_managed`) initialized from store on startup
- Root health endpoint at `/health` for liveness probes
- `DELETING` state for VMs and clusters during deletion
- Expanded VM status mapping for multi-hypervisor backends
- `ListClusters` now enriches responses with kweb version data
- Orphan deduplication in monitor to prevent repeated warnings
- `display_name`, `operations`, `metadata` in SPM registration

### Changed

- Health status values changed from `pass`/`fail` to `healthy`/`unhealthy`
- Cluster status `READY` changed to `ACTIVE` across codebase
- Structured logging switched to JSON output via `slog.NewJSONHandler`
- `doPost` kweb client uses `bytes.NewReader` instead of `strings.NewReader`
- Removed unused `Region` and `Zone` config fields

### Fixed

- `ResourcesManaged` gauge no longer resets to zero on restart
- Double `ListVMs` kweb call per poll cycle eliminated
- Health check metric blind spot when kweb is completely unreachable
- `ResourcesManaged` gauge drift when monitor cleans up disappeared resources
- Dead assignment `_ = pe` in `flushPending` removed
- Orphan counter no longer grows unboundedly across poll cycles

## [0.1.2] - 2026-06-10

### Fixed

- Lint error: replaced `bytes.NewBufferString(string(b))` with `bytes.NewBuffer(b)`

## [0.1.1] - 2026-06-10

### Added

- `provider_hints.kcli` pass-through for arbitrary kweb parameters (image, network, etc.)
- Cluster creation with configurable image parameter

### Fixed

- Cluster creation defaulting to unavailable `centos9stream` image

## [0.1.0] - 2026-05-01

### Added

- Initial release
- VM lifecycle (create, get, list, delete) via kweb
- Cluster lifecycle (create, get, list, delete) via kweb
- SPM registration with auto-retry
- NATS CloudEvent status publishing
- bbolt persistent state store
- OpenAPI-first request validation
- Configurable kweb profiles

[Unreleased]: https://github.com/pgarciaq/dcm-kcli-provider/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/pgarciaq/dcm-kcli-provider/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/pgarciaq/dcm-kcli-provider/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/pgarciaq/dcm-kcli-provider/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/pgarciaq/dcm-kcli-provider/releases/tag/v0.1.0
