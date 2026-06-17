# Adversarial Due Diligence Review — dcm-kcli-provider

## Version & Date

Version: 3.0 | Date: 2026-06-17 | Reviewer: AI-assisted | Codebase: commit `82300a6` (main)

Incremental review following v2.0 (commit `75cc3c3`). Two commits reviewed:
`bcc0f3b` (16 v2.0 finding fixes) and `82300a6` (13 performance fixes).

## Executive Summary

The two post-v2 commits materially improve readiness semantics, shutdown
ordering, RFC 7807 consistency, idempotent-create safety, and test coverage.
The performance pass (P1–P3) preserves all prior optimizations and adds
sensible wins (parallel monitor poll, health singleflight, kubeconfig cache).

11 of 16 v2 findings are **fully resolved**; 5 are **partially resolved**
(mostly where the fix chose documentation over tightening behavior). No v2
finding remains fully open. The performance work introduces a few real new
risks around kubeconfig caching and incomplete request-ID wiring.

All 11 "Do Not Regress" items remain **intact** — no regressions detected on
any prior performance or security hardening.

**Strengths:**
- Readiness gates on `Registered()` per registrar (atomic, 200/201 only)
- Clean shutdown: `Stop()` → wait → `Deregister()`
- Type-checked idempotent create, EqualFold status mapping, deterministic sort
- Good test additions for allowlist, body limit, RFC 7807, status, type conflicts
- Parallel monitor poll, singleflight health cache, kubeconfig cache

## Scorecard

| Dimension | v2.0 (post-fix) | v3.0 | Key gap |
|-----------|-----------------|------|---------|
| Security | ★★★★☆ | ★★★★☆ | Kubeconfig cache retention; allowlist documented but open |
| Correctness | ★★★★★ | ★★★★★ | Idempotent type check, EqualFold, deterministic sort |
| Auditability | ★★★★★ | ★★★★☆ | Request ID infra exists but not consumed in handlers |
| Operational | ★★★★★ | ★★★★☆ | Readiness fix strong; health cache asymmetry minor |
| Performance | ★★★★★ | ★★★★★ | All prior opts preserved; sensible additions |
| Design | ★★★★★ | ★★★★☆ | Monitor/server cache lifecycle split |
| Maintainability | ★★★★★ | ★★★★☆ | Good test growth; `/ready` path untested |
| Governance | ★★★★★ | ★★★★★ | govulncheck pinned |

## v2.0 Findings — Resolution Status

### Fully Resolved (11 of 16)

| # | Title | Status |
|---|-------|--------|
| NEW-01 | Readiness false-positive on reg failure | **Resolved** — `Registered()` via `atomic.Bool`, set only on 200/201 |
| NEW-02 | Registrar/deregister race on shutdown | **Resolved** — `Stop()` cancels context + waits before `Deregister()` |
| NEW-06 | Body limit non-RFC 7807 | **Resolved** — `RequestErrorHandlerFunc` detects `MaxBytesError` → 413 problem+json |
| NEW-07 | MapVMStatus mixed-case | **Resolved** — `strings.EqualFold` via `eqf()` helper |
| NEW-08 | Unstable sort on CreatedAt ties | **Resolved** — `sort.Slice` with ID tiebreaker (total order) |
| NEW-09 | Store type-tag canonical JSON | **Resolved** — documented in `store.go` |
| NEW-10 | Monitor metric over-counts | **Resolved** — help text clarified |
| NEW-12 | parseResponse unbounded body read | **Resolved** — `io.LimitReader(resp.Body, 10<<20)` |
| NEW-13 | Deregister doesn't reset reg metrics | **Resolved** — `RegistrationStatus.Set(0)` on DELETE |
| NEW-15 | govulncheck unpinned | **Resolved** — pinned to v1.3.0 |
| NEW-16 | Idempotent create ignores resource type | **Resolved** — 409 on type mismatch; tests added |

### Partially Resolved (5 of 16)

| # | Title | Gap |
|---|-------|-----|
| NEW-03 | Request ID not in structured logs | Middleware stores request_id in context; **handlers still use bare `s.logger`** |
| NEW-04 | Allowlist permits high-risk params | SECURITY NOTE added + warn on dropped keys; **high-risk keys still allowed** |
| NEW-05 | `keys` hint bypasses spec SSH path | Documented in SECURITY NOTE; **`keys` still in allowlist** |
| NEW-11 | Health cache stale-healthy | Trade-off documented; singleflight added; **accepted risk** |
| NEW-14 | No tests for new code paths | Tests added for body limit, helpers, status, type-check; **`/ready`, `Deregister`, health cache TTL still untested** |

## "Do Not Regress" Checklist

| Item | Status |
|------|--------|
| Parallel list fetches (WaitGroup) | ✅ Intact |
| Rate limiting (10/s burst 20) | ✅ Intact |
| Cluster create serialization (createMu) | ✅ Intact |
| Name index (bbolt name_index) | ✅ Intact |
| Pre-capped slices | ✅ Intact |
| Metrics RoutePattern | ✅ Intact |
| Response body limit (10 MB) | ✅ Intact |
| Orphan deduplication (seenOrphans) | ✅ Intact |
| NATS publish outside monitor mutex (M1) | ✅ Intact |
| Map size hints (M4) | ✅ Intact |
| Health cache (M7 + singleflight) | ✅ Intact + enhanced |

## New Findings (v3.0)

---

### V3-01: Kubeconfig cache not pruned on monitor-initiated cluster removal

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **Dimension** | Security |
| **Location** | `internal/api/server/impl.go` (`kcCache`), `internal/monitor/monitor.go:208-214` |
| **Description** | P2-05 added an in-memory kubeconfig cache (`sync.Map`). `DeleteCluster` and non-ACTIVE `GetCluster` evict entries, but when the **monitor** deletes a cluster from the store (kweb disappearance path), `kcCache` is never touched. |
| **Risk** | Cluster credentials (certs/keys in base64 kubeconfig) remain in heap indefinitely for monitor-deleted cluster IDs. Grows `sync.Map` without bound for churn scenarios. |
| **Recommendation** | Add cache eviction hook: pass a cache interface to the monitor, or subscribe monitor delete events to `kcCache.Delete(id)`. Also sweep expired entries periodically. |
| **Effort** | S |

---

### V3-02: Request ID middleware not wired into handler logging

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **Dimension** | Auditability |
| **Location** | `internal/handlers/request_logger.go`, `vm_handlers.go`, `cluster_handlers.go` |
| **Description** | P2-01 refactored to lazy context storage (good), but handlers still log via `s.logger.Warn(...)` without `handlers.LoggerFromContext(ctx, s.logger)`. The AUD-01 correlation goal remains unmet for the primary failure/rollback log paths. |
| **Risk** | Operators cannot correlate handler warnings (kweb failures, rollbacks) with chi request IDs in JSON logs. |
| **Recommendation** | Replace handler log calls with `handlers.LoggerFromContext(ctx, s.logger).Warn(...)`, or inject a request-scoped logger at handler entry. |
| **Effort** | S |

---

### V3-03: `/ready` endpoint has no automated tests

| Field | Value |
|-------|-------|
| **Severity** | Medium |
| **Dimension** | Maintainability |
| **Location** | `cmd/dcm-kcli-provider/server.go:338-375` |
| **Description** | NEW-01 fixed the highest-severity v2 bug, but there are zero tests covering readiness state transitions (kweb down, registration failed, poll incomplete, all-green). |
| **Risk** | A future refactor could reintroduce NEW-01 silently — this was the #1 priority v2 finding. |
| **Recommendation** | Add table-driven tests with mock registrars/monitor injecting `Registered()`, `pollComplete`, and kweb health states. |
| **Effort** | S |

---

### V3-04: Stale kubeconfig served for up to 5 minutes after credential rotation

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **Dimension** | Correctness |
| **Location** | `internal/api/server/cluster_handlers.go:290-312`, `impl.go:64` |
| **Description** | `getCachedKubeconfig` caches base64 kubeconfig + endpoint for 5 minutes with no invalidation on cluster state changes beyond delete/non-ACTIVE status. |
| **Risk** | After kweb rotates cluster credentials, API consumers receive stale kubeconfig until TTL expires. |
| **Recommendation** | Shorten TTL, invalidate on cluster status transition, or include a version/hash in cache key. |
| **Effort** | S |

---

### V3-05: `Registered()`, `Stop()`, and `Deregister()` lack integration tests

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **Dimension** | Maintainability |
| **Location** | `internal/registration/registrar.go` |
| **Description** | Registrar unit tests cover retry/success paths but not: `Registered()` false after 400/409, `Stop()` preventing post-deregister registration, or `Deregister()` resetting `RegistrationStatus` gauge. |
| **Risk** | NEW-01/02/13 could regress without CI signal. |
| **Recommendation** | Add tests asserting `Registered()==false` on failure, metrics gauge reset, and no SPM POST after `Stop()`. |
| **Effort** | S |

---

### V3-06: Health cache TTL behavior untested after singleflight refactor

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **Dimension** | Maintainability |
| **Location** | `internal/kweb/client.go:267-297` |
| **Description** | P2-03 added `singleflight.Group` to health checks. No test verifies TTL reuse, concurrent dedup, or stale windows. |
| **Risk** | Subtle concurrency or TTL bugs in the most-probed code path. |
| **Recommendation** | Add tests: two concurrent calls → one upstream request; second call within TTL → cached; after TTL → refresh. |
| **Effort** | S |

---

### V3-07: Duplicate RFC 7807 response helpers

| Field | Value |
|-------|-------|
| **Severity** | Informational |
| **Dimension** | Maintainability |
| **Location** | `internal/handlers/body_limit.go:19-36`, `internal/handlers/problem.go:9-27` |
| **Description** | P3-01 added typed `problemDetail` in `body_limit.go`; `problem.go` already has `ProblemDetail` + `WriteProblem`. Two parallel implementations with slightly different fields. |
| **Risk** | Response shape drift over time; duplicated maintenance. |
| **Recommendation** | Consolidate on one RFC 7807 writer used by body limit, panic recovery, and strict handler error funcs. |
| **Effort** | S |

---

### V3-08: Negative health results cached, delaying recovery detection

| Field | Value |
|-------|-------|
| **Severity** | Low |
| **Dimension** | Operational |
| **Location** | `internal/kweb/client.go:289-294` |
| **Description** | Both healthy and unhealthy results are cached for 5s. After kweb recovers, `/ready` may report `kweb:false` for up to 5s. |
| **Risk** | Brief unnecessary 503 on `/ready` after kweb recovery — usually harmless but extends rolling-deploy unready window. |
| **Recommendation** | Cache only successful checks; bypass cache on negative results. |
| **Effort** | S |

## Priority Remediation Order

| Priority | Finding | Effort | Rationale |
|----------|---------|--------|-----------|
| 1 | V3-03 | S | Test coverage for highest-severity v2 fix |
| 2 | V3-02 | S | Complete request-ID correlation for operators |
| 3 | V3-01 | S | Credential retention and unbounded cache growth |
| 4 | V3-05 | S | Prevent regression of v2 fixes |
| 5 | V3-04 | S | Kubeconfig cache invalidation |
| 6 | V3-06 | S | Health cache tests |
| 7 | V3-07 | S | Consolidate RFC 7807 helpers |
| 8 | V3-08 | S | Optional: asymmetric health cache |

## Current State

| Metric | Value |
|--------|-------|
| v1.0 findings (20) | All resolved |
| v2.0 findings (16) | 11 resolved, 5 partially resolved |
| New findings (v3.0) | 8 |
| Critical | 0 |
| High | 0 |
| Medium | 3 (V3-01, V3-02, V3-03) |
| Low | 4 (V3-04, V3-05, V3-06, V3-08) |
| Informational | 1 (V3-07) |
