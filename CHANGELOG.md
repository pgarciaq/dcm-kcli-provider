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
- `RequestLogger` middleware injects request_id into context-scoped slog (NEW-03)
- Idempotent create now validates resource type (returns 409 for type mismatch) (NEW-16)
- Tests for body limit, RFC 7807, request logger, mixed-case status, allowlist, type-check create (NEW-14)

### Changed

- List endpoints (`ListVMs`, `ListClusters`) now fetch store and kweb data in parallel (PERF-01)
- List results sorted by `CreatedAt` descending with ID tiebreaker via `sort.Slice` (COR-02, NEW-08, P2-02)
- Split `impl.go` (928 lines) into `vm_handlers.go`, `cluster_handlers.go`, `health_handlers.go`, `helpers.go` (DES-01)
- `startedAt` timestamp shared from `Server` to `StrictServerImpl` via `WithStartedAt` option (DES-02)
- Readiness probe uses `atomic.Bool` per registrar — only reports registered on HTTP 200/201 (NEW-01)
- Registrars stopped via `Stop()` before deregistration on shutdown (prevents re-register race) (NEW-02)
- Dropped provider_hints keys logged at warn level; risk documented per key (NEW-04/05)
- Body-too-large errors return RFC 7807 `application/problem+json` 413 response (NEW-06)
- `MapVMStatus` tolerates mixed-case kweb statuses via `strings.EqualFold` (zero allocation) (NEW-07, P1-01)
- `MonitorStatusChanges` metric help text clarifies coalesced debounce semantics (NEW-10)
- Deregister resets `RegistrationStatus` gauge to 0 (NEW-13)
- `govulncheck` pinned to v1.3.0 in CI (NEW-15)
- kweb `parseResponse` body read limited to 10 MB via `io.LimitReader` (NEW-12)
- `Server.Addr()` uses `sync.Map` for race-safe listener address access
- Monitor polls kweb concurrently (profiles, VMs, clusters in parallel goroutines) (P1-02)
- `RequestLogger` stores only request ID string in context (no per-request slog.Logger allocation) (P2-01)
- Health cache uses `singleflight` to deduplicate concurrent cache-miss kweb calls (P2-03)
- `lastPublish` debounce map pruned on resource deletion (prevents unbounded growth) (P2-04)
- `GetCluster` kubeconfig cached in-memory with 5-minute TTL (avoids per-GET kweb fetch) (P2-05)
- Removed duplicate chi `middleware.Logger` (single structured access log) (P2-06)
- `HasProfile()` method for O(1) profile lookup without slice copy (P2-07)
- RFC 7807 `WriteRFC7807` uses typed struct instead of `map[string]interface{}` (P3-01)
- `MaxBodySize` only wraps body on mutating HTTP methods (P3-02)
- `mergeKcliHints` uses inline comparison instead of allocating skip map (P3-03)
- Create handler params maps pre-sized with `make(map, 8)` (P3-04)

### Security

- Documented trusted-network-only deployment requirement (SEC-01)
- Documented TLS limitation with reverse-proxy recommendation (SEC-04)
- Documented Docker Compose SSH key mount risk with mitigation advice (SEC-05)
- Documented allowlist risk for cloudinit, cmdline, kernel, keys, iso, sharedfolders (NEW-04)

### Documentation

- Added Security Considerations section to README
- Added Known Limitations section (pagination, VM creation concurrency, single-instance) to README
- Added `/ready` and `/health` endpoints to API table
- Documented canonical JSON assumption in store type-tag pre-filter (NEW-09)
- Documented health cache trade-off (5s TTL vs staleness) (NEW-11)

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
