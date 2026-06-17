# Adversarial Due Diligence Review — dcm-kcli-provider

## Version & Date

Version: 2.0 | Date: 2026-06-17 | Reviewer: AI-assisted | Codebase: commit `75cc3c3` (main)

Incremental review following v1.0 (commit `6f177d6`). Two commits reviewed:
`c0d2905` (20 v1.0 finding fixes) and `75cc3c3` (12 performance fixes).

## Executive Summary

The v1.0 review identified 20 findings; 14 are now **properly resolved**, 6 are
**partially resolved** with residual gaps, and the fixes themselves introduced
16 new findings. The most serious regression is NEW-01: the `/ready` endpoint
can report `registered=true` when registrar goroutines exited due to permanent
failure (invalid UUID, 400, 409), undermining orchestrator integration.

The codebase is significantly stronger than v1.0: body size limits, request
correlation, parallel lists, sorted results, comprehensive Prometheus metrics,
readiness/deregistration lifecycle, file structure split, CHANGELOG, SECURITY.md,
and govulncheck CI are all in place. The remaining gaps are refinements, not
architectural problems.

**Key risks in priority order:**
1. Readiness false-positive on registration failure (NEW-01 — High)
2. Registrar/deregister race on shutdown (NEW-02 — Medium)
3. Request ID not in structured logs (NEW-03 — Medium)
4. Allowlist still permits high-risk kcli params (NEW-04 — Medium)
5. No tests for new security/operational code paths (NEW-14 — Medium)

## Scorecard

| Dimension | v1.0 | v2.0 | Change | Key gap |
|-----------|------|------|--------|---------|
| Security | ★★★☆☆ | ★★★★☆ | +1 | Allowlist permits cloudinit/cmdline/keys |
| Correctness | ★★★★☆ | ★★★★☆ | = | Unstable sort on CreatedAt ties |
| Auditability | ★★★★☆ | ★★★★☆ | = | Request ID not in slog output |
| Operational | ★★★★☆ | ★★★★☆ | = | Readiness false-positive on reg failure |
| Performance | ★★★★☆ | ★★★★★ | +1 | Health cache, parallel lists, mutex extraction |
| Design | ★★★★☆ | ★★★★★ | +1 | Clean file split, deduplicated startedAt |
| Maintainability | ★★★★★ | ★★★★☆ | -1 | New paths lack test coverage |
| Governance | ★★★☆☆ | ★★★★★ | +2 | CHANGELOG, SECURITY.md, govulncheck CI |

## v1.0 Findings — Resolution Status

### Properly Resolved (14 of 20)

| # | Title | Status |
|---|-------|--------|
| SEC-01 | No auth on API endpoints | Accepted (documented in README + SECURITY.md) |
| SEC-02 | No request body size limit | **Resolved** — `MaxBodySize(1<<20)` before OpenAPI validator |
| SEC-04 | No TLS configuration | Accepted (documented) |
| SEC-05 | docker-compose mounts host SSH keys | Accepted (documented) |
| COR-01 | Offset-based pagination | Accepted (documented in README) |
| COR-03 | No serialization for concurrent VM creates | Accepted (documented) |
| PERF-01 | Serial store+kweb in list endpoints | **Resolved** — parallel via `sync.WaitGroup`, no data races |
| PERF-02 | Pagination loads full dataset | Accepted (documented) |
| DES-01 | impl.go is 860+ lines | **Resolved** — split into 5 files (impl 124, vm 326, cluster 379, health 57, helpers 81) |
| DES-02 | Duplicate startedAt timestamps | **Resolved** — `WithStartedAt(s.startedAt)` |
| GOV-01 | No CHANGELOG.md | **Resolved** — full history v0.1.0 through Unreleased |
| GOV-02 | No SECURITY.md | **Resolved** — vulnerability reporting policy |
| GOV-03 | No vulnerability scanning in CI | **Resolved** — `govulncheck` job added |
| OPS-02 | Shutdown does not deregister | **Resolved** — best-effort DELETE with 5s timeout (race caveat: NEW-02) |

### Partially Resolved (6 of 20)

| # | Title | Gap |
|---|-------|-----|
| SEC-03 | provider_hints allowlist | Allowlist blocks `cmds`/`scripts`/`files` but permits `cloudinit`, `cmdline`, `keys` (NEW-04, NEW-05) |
| AUD-01 | No request correlation ID | `middleware.RequestID` installed but not propagated to slog JSON (NEW-03) |
| AUD-02 | Registration metrics | Gauges and counters work; readiness doesn't gate on them; deregister doesn't reset gauge (NEW-13) |
| AUD-03 | Monitor poll metrics | Timing correct; counter over-counts coalesced debounces (NEW-10) |
| OPS-01 | No readiness probe | `/ready` exists but `registered` flag triggers on failure too (NEW-01) |
| COR-02 | Non-deterministic list ordering | Sort applied but unstable for CreatedAt ties (NEW-08) |

## New Findings (v2.0)

---

### NEW-01: Readiness reports registered=true when registration failed

| Field | Value |
|-------|-------|
| Severity | High |
| Dimension | Operational |
| Location | `cmd/dcm-kcli-provider/server.go:242-247` |
| Description | The `registered` channel closes when any registrar's `Done()` fires. `Done()` closes on **both success and permanent failure** (invalid UUID, 400, 409). The readiness handler treats channel closure as "registered with SPM." A provider that permanently failed registration reports `{"ready":true, "registered":true}` if kweb is healthy and first poll completed. |
| Risk | Orchestrators route traffic to a provider invisible to the DCM control plane. Resources created via API exist in bbolt but are never discovered by SPM. |
| Recommendation | Track per-registrar success via `atomic.Bool` set only on 200/201 in `register()`. Readiness should require both registrars succeeded. Alternatively, gate readiness on `RegistrationStatus` gauge being 1 for both providers. |
| Effort | S |

---

### NEW-02: Registration goroutines race with deregister on shutdown

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Operational |
| Location | `cmd/dcm-kcli-provider/server.go:238-240,290-295` |
| Description | Registrars use the parent context from `Start(ctx)`. On SIGTERM, `shutdown()` calls `Deregister()` while registrar retry loops may still be active (waiting on backoff timer with the original context). A registrar could succeed its `CreateProvider` call **after** `Deregister()` completed, leaving a stale SPM entry. |
| Risk | SPM contains a registered provider pointing to a stopped endpoint until health-check timeout. |
| Recommendation | Cancel the registrar context at the start of `shutdown()`, before calling `Deregister()`. Add a `Stop()` method to `Registrar` that cancels its internal context and waits for the goroutine to exit. |
| Effort | S |

---

### NEW-03: Request ID not propagated to structured logs

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Auditability |
| Location | `cmd/dcm-kcli-provider/server.go:173` |
| Description | `middleware.RequestID` is installed and chi's text `middleware.Logger` includes it, but the application's `slog.NewJSONHandler` has no wrapper to inject `request_id` from `middleware.GetReqID(ctx)`. All handler-level `s.logger.Info/Warn/Error` calls lack request correlation. |
| Risk | The primary goal of AUD-01 (cross-layer request correlation in structured logs) is not achieved. Operators must match chi text logs to slog JSON logs by timestamp. |
| Recommendation | Add a chi middleware that creates a request-scoped `slog.Logger` with `request_id` attribute and injects it into the context, or wrap the `slog.Handler` to extract the request ID. |
| Effort | S |

---

### NEW-04: Provider hints allowlist permits high-risk kcli parameters

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Security |
| Location | `internal/api/server/helpers.go:22-35` |
| Description | The allowlist blocks the most dangerous keys (`cmds`, `scripts`, `files`) but still permits `cloudinit` (arbitrary cloud-init userdata), `cmdline` (kernel boot parameters), `kernel`/`initrd` (arbitrary kernel images), `iso` (arbitrary ISO mount), `keys` (SSH key injection), `sharedfolders` (host path exposure), and `yamlinventory` (arbitrary YAML config). |
| Risk | A caller on the trusted network can inject arbitrary cloud-init scripts, kernel boot parameters, or SSH keys via these parameters. Weaker than SEC-03's original intent of limiting to "safe sizing/network" parameters only. |
| Recommendation | Reduce allowlist to clearly safe keys: `image`, `network`, `pool`, `numcpus`, `memory`, `disks`, `nets`, `dns`, `domain`, `start`, `autostart`, `flavor`, `virttype`, `tags`, `ctlplanes`, `workers`, `version`, `sdn`, `network_type`, `api_ip`. Remove `cloudinit`, `cmdline`, `kernel`, `initrd`, `iso`, `keys`, `sharedfolders`, `yamlinventory`. Log dropped keys at warn level. |
| Effort | S |

---

### NEW-05: `keys` hint bypasses spec SSH key path

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Security |
| Location | `internal/api/server/vm_handlers.go:62-65`, `helpers.go:56-62` |
| Description | The VM spec has a dedicated `ssh_keys` field mapped to `params["keys"]`. The allowlist also permits `keys` as a provider hint. `mergeKcliHints` skips keys that already exist in `params`, but the spec path uses `"keys"` (same key), so there's no conflict in the happy path. However, if the spec omits `ssh_keys`, the hint `keys` value passes through directly, allowing unauthorized SSH key injection. |
| Risk | Attacker specifies `provider_hints.kcli.keys` to inject SSH keys outside the documented catalog spec, bypassing any future validation on the spec's `ssh_keys` field. |
| Recommendation | Remove `keys` from the allowlist. SSH keys should only come from the spec's `ssh_keys` field. |
| Effort | S |

---

### NEW-06: Body limit returns non-RFC 7807 error response

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Correctness |
| Location | `internal/handlers/body_limit.go`, generated `server.gen.go` |
| Description | When `MaxBytesReader` triggers, the JSON decoder in the generated strict handler returns `http: request body too large`. This surfaces as a 400 plain text error from the OpenAPI validator, not a `application/problem+json` 413 response. |
| Risk | API clients expecting consistent RFC 7807 responses may fail to parse the body-too-large error. Memory protection works correctly regardless. |
| Recommendation | Add a custom `RequestErrorHandlerFunc` that detects `http.MaxBytesError` and returns a 413 problem+json response. |
| Effort | S |

---

### NEW-07: MapVMStatus no longer tolerates mixed-case kweb statuses

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Correctness |
| Location | `internal/monitor/status.go:9-26` |
| Description | Performance fix M6 removed `strings.ToLower`, leaving exact-match switch cases. If kweb or a different libvirt backend ever returns `"Up"`, `"Running"`, or `"Shutoff"` (capitalized), the default case maps to `ERROR`. |
| Risk | False ERROR states and spurious NATS events after a kweb or hypervisor upgrade. Currently safe because kweb returns lowercase, but brittle. |
| Recommendation | Use `strings.EqualFold` per case arm (zero-alloc for ASCII comparison), or add a warn-level log in the default case with the unrecognized status. |
| Effort | S |

---

### NEW-08: Unstable sort breaks pagination when CreatedAt ties

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Correctness |
| Location | `internal/api/server/vm_handlers.go:157`, `cluster_handlers.go:155` |
| Description | `sort.Slice` is not stable. When multiple resources have identical `CreatedAt` (batch import, clock resolution, zero value), their relative order varies between calls. Combined with offset pagination, this amplifies COR-01's skip/duplicate behavior. |
| Risk | Inconsistent list responses within a single paginated traversal. |
| Recommendation | Use `sort.SliceStable`, or add a secondary sort key (`ID`) to guarantee deterministic ordering. |
| Effort | S |

---

### NEW-09: Store type-tag pre-filter relies on canonical JSON

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Correctness |
| Location | `internal/store/store.go:15-16,157-158` |
| Description | The M2 performance fix uses `bytes.Contains(v, typeTagVM)` where `typeTagVM = []byte(`"type":"vm"`)`. This works because `json.Marshal` produces canonical JSON without spaces. If the store format is ever modified by an external tool (pretty-printed, field-reordered), the filter would skip valid entries, returning empty lists. |
| Risk | Subtle data disappearance if store migration or external tool modifies JSON format. The post-filter `entry.Type == resourceType` catches false positives but cannot recover false negatives. |
| Recommendation | Accept for now (internal format controlled by `json.Marshal`). Add a comment documenting the canonical-JSON assumption. Consider secondary type buckets in a future store schema migration. |
| Effort | S (comment) / M (buckets) |

---

### NEW-10: MonitorStatusChanges over-counts debounced events

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Auditability |
| Location | `internal/monitor/monitor.go:209` |
| Description | `metrics.MonitorStatusChanges.Inc()` fires on every `publishWithDebounce` call, including when an existing pending event for the same ID is overwritten within the debounce window. This counts "status update attempts," not unique published transitions. |
| Risk | Misleading metrics: dashboard shows 50 "status changes" when only 10 events were actually published. |
| Recommendation | Rename help text to "Total status change events scheduled (including coalesced)" or only increment when the status string actually differs from the existing pending event. |
| Effort | S |

---

### NEW-11: Health cache serves stale "healthy" after kweb failure

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Operational |
| Location | `internal/kweb/client.go:260-278` |
| Description | The 5-second health cache (M7) stores both healthy and unhealthy results. If kweb becomes unreachable immediately after a successful check, `/health` and `/ready` report healthy for up to 5 seconds. |
| Risk | Brief false-healthy window during kweb outage. Acceptable for liveness/readiness probes (Kubernetes probes tolerate a few seconds of stale data). |
| Recommendation | Accept as documented trade-off. Optionally, cache only healthy results and always re-check on unhealthy (negative cache miss). |
| Effort | S |

---

### NEW-12: parseResponse reads POST/DELETE bodies without size limit

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Security |
| Location | `internal/kweb/client.go:397-401` |
| Description | `doGet` uses `io.LimitReader(resp.Body, 10<<20)` but `parseResponse` (used by `doPost` and `doDelete`) uses unbounded `io.ReadAll(resp.Body)`. A malicious kweb could return a large response body on POST/DELETE, exhausting provider memory. |
| Risk | Memory exhaustion from upstream response. Requires compromised kweb (unlikely in homelab). |
| Recommendation | Add `io.LimitReader` to `parseResponse` consistently with `doGet`. |
| Effort | S |

---

### NEW-13: Deregister does not reset registration metrics

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Auditability |
| Location | `internal/registration/registrar.go:182-199` |
| Description | On successful deregistration, `RegistrationStatus` gauge stays at 1. If another system scrapes `/metrics` during the shutdown window (between deregister and process exit), it shows "registered" for a provider that no longer exists in SPM. |
| Risk | Misleading metrics during rolling deploys or shutdown. |
| Recommendation | Add `metrics.RegistrationStatus.WithLabelValues(r.providerCfg.Name).Set(0)` on successful DELETE. |
| Effort | S |

---

### NEW-14: No automated tests for new security/operational paths

| Field | Value |
|-------|-------|
| Severity | Medium |
| Dimension | Maintainability |
| Location | Multiple new files without corresponding test coverage |
| Description | The following new code paths lack test coverage: (1) `MaxBodySize` middleware rejecting >1MB bodies, (2) `allowedKcliHintKeys` dropping dangerous keys, (3) `/ready` endpoint state transitions, (4) `Deregister` when SPM unreachable, (5) `WithStartedAt` uptime consistency, (6) `sort.Slice` by `CreatedAt`, (7) registration/monitor Prometheus metrics values, (8) health cache TTL behavior. |
| Risk | Regressions in the exact code paths added to fix v1.0 findings will go unnoticed. The existing test suite covers the pre-fix behavior but not the new additions. |
| Recommendation | Add focused unit tests for each new feature. Priority: body limit, hint allowlist, readiness states, sort ordering. |
| Effort | M |

---

### NEW-15: govulncheck version unpinned in CI

| Field | Value |
|-------|-------|
| Severity | Informational |
| Dimension | Governance |
| Location | `.github/workflows/ci.yaml:37` |
| Description | `go install golang.org/x/vuln/cmd/govulncheck@latest` installs whatever version is current at build time. |
| Risk | Non-reproducible CI runs. A govulncheck update could introduce false positives that block unrelated PRs, or false negatives that miss real vulnerabilities. |
| Recommendation | Pin to a specific version (e.g., `govulncheck@v1.1.4`). Update periodically as part of dependency maintenance. |
| Effort | S |

---

### NEW-16: Idempotent create does not verify resource type

| Field | Value |
|-------|-------|
| Severity | Low |
| Dimension | Correctness |
| Location | `internal/api/server/vm_handlers.go:22-27`, `cluster_handlers.go:25-29` |
| Description | The idempotency check calls `store.Get(id)` and returns the existing entry if found. It does not verify that the existing entry's `Type` matches the endpoint (e.g., a cluster ID used on the VM endpoint returns a 201 with mismatched data). |
| Risk | Confusing API responses if IDs collide across types. Unlikely with UUID generation but possible if client provides `?id=` manually. |
| Recommendation | Add `existing.Type != "vm"` / `"cluster"` check and return 409 Conflict. |
| Effort | S |

---

## Findings Status Summary

| # | Title | Severity | Dimension | Status |
|---|-------|----------|-----------|--------|
| NEW-01 | Readiness false-positive on reg failure | High | Operational | Open |
| NEW-02 | Registrar/deregister race on shutdown | Medium | Operational | Open |
| NEW-03 | Request ID not in structured logs | Medium | Auditability | Open |
| NEW-04 | Allowlist permits high-risk kcli params | Medium | Security | Open |
| NEW-05 | `keys` hint bypasses spec SSH key path | Medium | Security | Open |
| NEW-06 | Body limit returns non-RFC 7807 error | Low | Correctness | Open |
| NEW-07 | MapVMStatus rejects mixed-case statuses | Low | Correctness | Open |
| NEW-08 | Unstable sort on CreatedAt ties | Low | Correctness | Open |
| NEW-09 | Store type-tag assumes canonical JSON | Low | Correctness | Open |
| NEW-10 | Monitor metric over-counts coalesced events | Low | Auditability | Open |
| NEW-11 | Health cache stale-healthy window | Low | Operational | Open |
| NEW-12 | parseResponse unbounded body read | Low | Security | Open |
| NEW-13 | Deregister doesn't reset reg metrics | Low | Auditability | Open |
| NEW-14 | No tests for new security/ops paths | Medium | Maintainability | Open |
| NEW-15 | govulncheck version unpinned | Informational | Governance | Open |
| NEW-16 | Idempotent create ignores resource type | Low | Correctness | Open |

## Priority Remediation Order

| Priority | Finding | Effort | Impact |
|----------|---------|--------|--------|
| 1 | NEW-01: Fix readiness registration check | S | Prevents false-ready on reg failure |
| 2 | NEW-02: Cancel registrars before deregister | S | Prevents re-registration after deregister |
| 3 | NEW-04+05: Tighten allowlist, remove `keys` | S | Closes SEC-03 residual gap |
| 4 | NEW-03: Propagate request ID to slog | S | Completes AUD-01 |
| 5 | NEW-14: Add tests for new code paths | M | Prevents regressions |
| 6 | NEW-07: Tolerate mixed-case VM status | S | Defensive correctness |
| 7 | NEW-08: Stable sort with secondary key | S | Pagination consistency |
| 8 | NEW-12: Limit parseResponse body read | S | Consistent upstream protection |
| 9 | NEW-13: Reset reg metrics on deregister | S | Metric accuracy |
| 10 | NEW-10: Fix monitor metric semantics | S | Dashboard accuracy |
| 11 | NEW-16: Type-check idempotent create | S | API correctness |
| 12 | NEW-06: RFC 7807 for body-too-large | S | API consistency |
| 13 | NEW-09: Document canonical JSON assumption | S | Maintenance |
| 14 | NEW-15: Pin govulncheck version | S | CI reproducibility |
| 15 | NEW-11: Accept health cache trade-off | S | Document |

## Accepted Risks (carried from v1.0)

| Finding | Rationale |
|---------|-----------|
| SEC-01 (No auth) | Homelab scope. Documented in README + SECURITY.md. |
| SEC-04 (No TLS) | Homelab scope. Documented. |
| SEC-05 (SSH key mounts) | Dev-only docker-compose. Documented. |
| COR-01 (Offset pagination) | ~200 max resources. Documented. |
| COR-03 (VM create not serialized) | kweb handles conflicts. Documented. |
| PERF-02 (Full dataset load) | ~40 KB at max scale. Documented. |

## Current State

- **v1.0 findings:** 20 total → 14 resolved, 6 partially resolved
- **v2.0 new findings:** 16 (0 Critical, 1 High, 4 Medium, 8 Low, 1 Informational, 2 Accepted)
- **Regressions from fixes:** 5 (NEW-01, NEW-02, NEW-07, NEW-10, NEW-11)
- **Accepted risks:** 6 (unchanged from v1.0)

---

*Incremental review performed 2026-06-17 against commit `75cc3c3` on `main`.
Updates v1.0 review from commit `6f177d6` (see `adversarial-review-2026-06-17.md`
for original findings).*
