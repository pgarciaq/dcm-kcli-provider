# Performance Audit Report: dcm-kcli-provider

## Date and Scope

**Date:** 2026-06-17 (v2, post-adversarial-review fixes)
**Codebase:** HEAD of main (post v2 adversarial fixes: NEW-01 through NEW-16)
**Prior audit:** Same day, commit `c0d2905` — all 12 actionable findings (M1–M11, M15) were implemented
**Scope:** Full codebase — API handlers, kweb client, bbolt store, monitor loop,
NATS publisher, metrics middleware, registration, new security/ops code

## Overall Assessment

The dcm-kcli-provider remains **I/O-bound**. The v2 adversarial fixes added
correctness and observability code with minimal performance impact. One prior
optimization (M6: remove `strings.ToLower`) was intentionally reverted for
correctness (NEW-07: tolerate mixed-case kweb statuses) — this is the only
regression and has a zero-allocation fix via `strings.EqualFold`.

**Steady-state resource profile (200 VMs, 50 clusters, 30s poll):**

| Metric | Estimate |
|--------|----------|
| RSS | ~15–25 MB (Go runtime + bbolt mmap + HTTP buffers) |
| CPU | <1% (idle between polls, trivial per-request work) |
| Goroutines | 6 steady-state (main, HTTP, monitor, 2 registrars, signal) |
| Poll I/O | 3 kweb GETs (serial) + 2 bbolt scans + 0–N NATS publishes per 30s |
| bbolt file | <1 MB at 250 entries × ~150 bytes/entry |

No P0 (critical) findings. Two P1 items are actionable quick wins.

## What Is Working Well (Do Not Regress)

1. **Parallel list fetches** — `ListVMs`/`ListClusters` fetch store and kweb
   concurrently via `sync.WaitGroup`. This halves list latency.
2. **Rate limiting** — kweb client rate-limits at 10 req/s burst 20, preventing
   thundering-herd issues during rapid API calls.
3. **Cluster create serialization** — `createMu` prevents concurrent kweb
   cluster creates that would overwhelm the hypervisor.
4. **Name index** — `FindByKcliName` uses a bbolt secondary index bucket
   (`name_index`) for O(1) lookups, not full scans.
5. **Pre-capped slices** — `make([]VM, 0, len(storeVMs))` in list handlers and
   store `Stats().KeyN` pre-caps avoid realloc.
6. **Metrics route pattern** — Uses `chi.RouteContext().RoutePattern()` instead
   of `r.URL.Path` to avoid unbounded Prometheus label cardinality.
7. **Response body limit** — `io.LimitReader(resp.Body, 10<<20)` caps kweb
   response reads at 10 MB.
8. **Orphan deduplication** — `seenOrphans` map prevents repeated logging/counting
   of the same orphan across poll cycles.
9. **NATS publish outside monitor mutex** — (M1 fix) Events collected under lock,
   published after releasing lock.
10. **Map size hints** — (M4 fix) All kweb maps allocated with `len(kwebVMs)`.
11. **`fmt.Sprintf` → string concat** — (M3 fix) `"vms/" + entry.ID` in entry builders.
12. **`MaxIdleConnsPerHost: 10`** — (M10 fix) kweb HTTP transport reuses connections.
13. **`bytes.Contains` pre-filter** — (M2/M11 fix) Store list and parseResponse
    skip full unmarshal when pattern absent.
14. **Health cache (5s TTL)** — (M7 fix) Deduplicates kweb health checks.
15. **Orphan batch detection** — (M15 fix) Uses existing store data, no per-VM
    bbolt transactions.

## Prior Audit Status

| ID | Status | Notes |
|----|--------|-------|
| M1 | ✅ Implemented | NATS publish outside lock |
| M2 | ✅ Implemented (Option C) | `bytes.Contains` pre-filter |
| M3 | ✅ Implemented | String concat |
| M4 | ✅ Implemented | Map size hints |
| M5 | ✅ Implemented | `Stats().KeyN` pre-cap |
| M6 | ⚠️ **Reverted** | `strings.ToLower` re-added for NEW-07 correctness (see P1-01) |
| M7 | ✅ Implemented | Health cache |
| M8 | ✅ Implemented | Context passed to `flushOne` |
| M9 | ✅ Implemented | Publisher checks `ctx.Err()` |
| M10 | ✅ Implemented | Transport tuning |
| M11 | ✅ Implemented | `bytes.Contains` fast-path |
| M15 | ✅ Implemented | Orphan batch detection |
| M12 | Deferred | `sync.Pool` for statusWriter — not needed at <10 req/s |
| M13 | Deferred | UUID pool — not needed at homelab event rates |
| M14 | Deferred | Binary bbolt encoding — not needed at <500 entries |

## Findings by Priority

### P1 — High

#### P1-01: `strings.ToLower` allocation in MapVMStatus (M6 regression)

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/status.go:14` |
| **Current state** | `strings.ToLower(kwebStatus)` allocates a new string on every call. Called once per VM per poll cycle (~200 allocations/30s). Was removed in M6, re-added for NEW-07 correctness (tolerate mixed-case). |
| **Proposed fix** | Replace the `switch strings.ToLower(s)` with per-case `strings.EqualFold` checks. Zero allocation for ASCII comparisons. |
| **Expected impact** | Eliminates ~200 string allocations per poll cycle. |
| **Risk** | None. `strings.EqualFold` is case-insensitive without allocation. |
| **Effort** | S (minutes) |

#### P1-02: Monitor polls kweb sequentially (3 serial RTTs)

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/monitor.go:97–106` |
| **Current state** | Each 30s cycle: `refreshProfiles` → `pollVMs` → `pollClusters` → `flushPending` → `detectVMOrphans`. Three kweb GETs run in sequence. Under LAN conditions (~5ms RTT), this adds ~15ms vs ~5ms if parallelized. |
| **Proposed fix** | Run `refreshProfiles`, `pollVMs`, and `pollClusters` concurrently using `errgroup`. The rate limiter will naturally sequence them if burst is exhausted, but under normal conditions (3 tokens needed, burst=20) all three proceed immediately. |
| **Expected impact** | Reduces poll cycle wall-clock by ~10ms (2 RTTs). More significant on higher-latency networks. |
| **Risk** | Low. Each function writes to independent store buckets. bbolt serializes writes naturally. |
| **Effort** | S (hours) |

### P2 — Medium

#### P2-01: RequestLogger allocates child slog.Logger per request

| Field | Detail |
|-------|--------|
| **Location** | `internal/handlers/request_logger.go:17–21` |
| **Current state** | `base.With("request_id", reqID)` creates a new `slog.Logger` instance per request. Currently, handler code uses `s.logger` directly, not `LoggerFromContext(ctx)`, so the injected logger is unused in hot paths — pure overhead. |
| **Proposed fix** | **Option A:** Remove RequestLogger middleware; store only the `request_id` string in context; use `slog.With("request_id", id)` at log sites that need it. **Option B:** Wire all handler logging through `LoggerFromContext(ctx)` to justify the allocation. |
| **Expected impact** | Eliminates 1 heap allocation per request (slog.Logger + attrs). |
| **Risk** | Low. |
| **Effort** | S |

#### P2-02: `sort.SliceStable` allocates O(n) auxiliary memory

| Field | Detail |
|-------|--------|
| **Location** | `internal/api/server/vm_handlers.go:163–168`, `cluster_handlers.go:161–166` |
| **Current state** | v2 fix uses `sort.SliceStable` with composite key (CreatedAt, ID) for deterministic pagination. Stable sort allocates an auxiliary slice of ~n pointers. |
| **Proposed fix** | Use `sort.Slice` (unstable) with the same composite key. Since the secondary key (ID) is unique, the sort produces identical results regardless of stability — the comparator is a total order. |
| **Expected impact** | Eliminates O(n) scratch allocation per list. |
| **Risk** | None — total ordering means unstable sort produces deterministic results. |
| **Effort** | S (minutes) |

#### P2-03: Health cache stampede on TTL expiry

| Field | Detail |
|-------|--------|
| **Location** | `internal/kweb/client.go:265–284` |
| **Current state** | When 5s TTL expires, concurrent callers (`/health`, `/ready`, API health) all miss cache and each independently calls kweb. |
| **Proposed fix** | Use `golang.org/x/sync/singleflight` to deduplicate concurrent uncached health checks. |
| **Expected impact** | Collapses N concurrent cache-miss health checks to 1 kweb request. |
| **Risk** | Low. |
| **Effort** | S (hours) |

#### P2-04: `lastPublish` debounce map never pruned

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/monitor.go:40,211` |
| **Current state** | `lastPublish[id]` is set on every publish; entries are never deleted when resources are removed. Over months with VM/cluster churn, the map grows unboundedly with historical IDs (~48 bytes per entry: UUID string + time.Time). |
| **Proposed fix** | Delete `lastPublish[id]` when the monitor detects resource deletion (in `pollVMs`/`pollClusters` delete paths). |
| **Expected impact** | Prevents unbounded memory growth. At 10 creates/day × 365 days = ~175 KB wasted. |
| **Risk** | Low. Debounce window resets correctly for recreated IDs. |
| **Effort** | S (minutes) |

#### P2-05: GetCluster fetches and parses kubeconfig on every request

| Field | Detail |
|-------|--------|
| **Location** | `internal/api/server/cluster_handlers.go:226–236, 294–331` |
| **Current state** | For ACTIVE clusters: HTTP GET to kweb for kubeconfig, base64 encode, YAML unmarshal to extract API endpoint — all per `GET /clusters/{id}` request. |
| **Proposed fix** | Cache the kubeconfig + extracted endpoint in the store entry when the monitor first detects ACTIVE status. Serve from cache on GET. Invalidate when status changes away from ACTIVE. |
| **Expected impact** | Eliminates 1 kweb RTT + YAML parse + base64 encode per GetCluster call. |
| **Risk** | Medium — stale kubeconfig if cluster rotates certs. Add TTL-based invalidation. |
| **Effort** | M (days) |

#### P2-06: Duplicate access logging middleware

| Field | Detail |
|-------|--------|
| **Location** | `cmd/dcm-kcli-provider/server.go:184–186` |
| **Current state** | Both `handlers.RequestLogger` (slog child) and chi `middleware.Logger` (text access log) are installed. Every request pays for two log writes — one structured (unused), one text. |
| **Proposed fix** | Remove `middleware.Logger`. The structured `RequestLogger` provides the same information. If text logging is still desired, write a single structured access log in `RequestLogger` on response completion. |
| **Expected impact** | Eliminates 1 formatted string write + buffer allocation per request. |
| **Risk** | Low. |
| **Effort** | S (minutes) |

#### P2-07: `Profiles()` copies entire profile slice per VM create

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/monitor.go:67–72`, used from `vm_handlers.go:38–46` |
| **Current state** | `Profiles()` locks mutex, allocates a new slice, copies all profile names, returns copy. CreateVM then linear-scans the copy to validate the requested profile. Profiles rarely change (refreshed every 30s from kweb). |
| **Proposed fix** | Add `HasProfile(name string) bool` method that checks under lock without copying: `m.mu.RLock(); _, ok := m.profileMap[name]; m.mu.RUnlock(); return ok`. Store profiles as `map[string]struct{}` in addition to the slice. |
| **Expected impact** | Eliminates O(profiles) copy + scan per create. |
| **Risk** | None. |
| **Effort** | S (hours) |

### P3 — Low

#### P3-01: `WriteRFC7807` allocates `map[string]interface{}`

| Field | Detail |
|-------|--------|
| **Location** | `internal/handlers/body_limit.go:21–26` |
| **Current state** | Builds a map per error response and encodes to JSON. |
| **Proposed fix** | Use a typed struct: `type problemDetail struct { Type string; Title string; Status int; Detail string }`. Avoids map boxing. |
| **Expected impact** | 1 map allocation per error response eliminated. Cold path. |
| **Risk** | None. |
| **Effort** | S (minutes) |

#### P3-02: `MaxBodySize` wraps body on all methods including GET

| Field | Detail |
|-------|--------|
| **Location** | `internal/handlers/body_limit.go`, wired in `server.go:189` |
| **Current state** | `http.MaxBytesReader` wraps body on every request, including GETs (which have no body). Overhead is one struct wrapper — cheap but unnecessary. |
| **Proposed fix** | Apply only to POST/PUT/PATCH/DELETE methods. |
| **Expected impact** | Eliminates 1 wrapper allocation on GET/list/health requests. |
| **Risk** | None. |
| **Effort** | S (minutes) |

#### P3-03: `mergeKcliHints` allocates skip map on every create

| Field | Detail |
|-------|--------|
| **Location** | `internal/api/server/helpers.go:67–70` |
| **Current state** | `make(map[string]bool, len(excludeKeys))` per create. Typically 1–2 exclude keys. |
| **Proposed fix** | For 1–2 keys, use inline string comparison instead of map. |
| **Expected impact** | 1 small map allocation per create eliminated. Negligible at create rate. |
| **Risk** | None. |
| **Effort** | S (minutes) |

#### P3-04: Create handler params map without size hint

| Field | Detail |
|-------|--------|
| **Location** | `vm_handlers.go:55`, `cluster_handlers.go:56` |
| **Current state** | `params := map[string]interface{}{}` grows via rehashing. |
| **Proposed fix** | `make(map[string]interface{}, 8)` (typical param count). |
| **Expected impact** | 1–2 fewer map reallocations per create. |
| **Risk** | None. |
| **Effort** | S (minutes) |

#### P3-05: OpenAPI validation runs on health endpoints

| Field | Detail |
|-------|--------|
| **Location** | `cmd/dcm-kcli-provider/server.go:190–195` |
| **Current state** | `OapiRequestValidatorWithOptions` validates every request, including GET `/vms/health` and `/clusters/health`. These simple GETs don't need schema validation. |
| **Proposed fix** | Add health paths to the validator's `Skipper` function. |
| **Expected impact** | Eliminates schema validation CPU on health probes. |
| **Risk** | None for parameterless GETs. |
| **Effort** | S (minutes) |

## Deferred Items — Revisit Triggers

| ID | Item | Revisit When |
|----|------|--------------|
| D1 | Binary encoding for bbolt (M14) | Resource count exceeds 1,000 or poll latency exceeds 500ms |
| D2 | `sync.Pool` for `statusWriter` (M12) | Request rate exceeds 100 req/s sustained |
| D3 | Separate bbolt buckets per type (M2 Option B) | Resource count exceeds 500 or store list latency exceeds 50ms |
| D4 | HTTP/2 to kweb | kweb adds HTTP/2 support |
| D5 | Streaming JSON responses | List response sizes exceed 1 MB |
| D6 | Async NATS publisher with channel buffer | Event rate exceeds 50/s or NATS latency exceeds 100ms |
| D7 | Batch monitor store writes (single txn) | Status transitions exceed 50/poll or fsync latency measured |
| D8 | Store context propagation | Store scans exceed 10ms (>1000 entries) |
| D9 | `ListClusters` double-unmarshal optimization | Cluster count exceeds 200 |

## Accuracy Trade-off Register

No trade-offs applicable. This service manages infrastructure lifecycle state
(discrete status values, integer resource counts). There are no floating-point
computations, statistical calculations, or approximation opportunities.

The only `float64`/`float32` usage is:

| Location | Usage | Impact |
|----------|-------|--------|
| `server.go:79–80` | `float64(count)` for Prometheus gauge init | Exact for integers < 2^53 |
| `server.go:371` | `float32(uptime_seconds)` in health response | Loses precision after ~16M seconds (~185 days). Acceptable for display-only field. |
| `metrics.go:98` | `time.Since().Seconds()` for histogram | Standard Prometheus pattern, no alternative. |
| `kweb/client.go:78` | `rate.Limit(10)` | Rate limiter internal, exact at integer rates. |

None of these are on the data plane or affect correctness.

## Appendix: Call Count Estimates

**Per 30-second monitor poll cycle (200 VMs, 50 clusters, 0 status changes):**

| Operation | Count | Dominant Cost |
|-----------|-------|---------------|
| `kweb.ListProfiles` (HTTP GET) | 1 | Network RTT (~5ms LAN) |
| `kweb.ListVMs` (HTTP GET) | 1 | Network RTT + JSON unmarshal (~200 entries) |
| `kweb.ListClusters` (HTTP GET) | 1 | Network RTT + double JSON unmarshal (~50 entries) |
| `store.List("vm")` (bbolt ForEach) | 1 | 250 entries × bytes.Contains + ~200 json.Unmarshal |
| `store.List("cluster")` (bbolt ForEach) | 1 | 250 entries × bytes.Contains + ~50 json.Unmarshal |
| `MapVMStatus` | ~200 | `strings.ToLower` + switch (1 alloc each) |
| `MapClusterStatus` | ~50 | Comparison only |
| `map` creation | 4 | Sized: `make(map, len(kweb*))` |
| Prometheus observe | 1 | `MonitorPollDuration` histogram |
| Total allocations | ~700 | ~250 json.Unmarshal + ~200 ToLower + maps + slice pre-caps |

**Per API list request (200 VMs):**

| Operation | Count | Dominant Cost |
|-----------|-------|---------------|
| `store.List("vm")` | 1 (goroutine) | ~200 json.Unmarshal |
| `kweb.ListVMs` | 1 (goroutine, parallel) | Network RTT + body read |
| `sort.SliceStable` | 1 | O(n log n) + O(n) aux memory |
| `entryToVM` | ~200 | String concat + struct construction |
| Map build | 1 | ~200 insertions (sized) |
| `statusWriter` | 1 | Heap escape |
| `slog.Logger` child | 1 | Heap escape (unused by handlers) |
| JSON response marshal | 1 | ~200 VM structs |

## ROI Prioritization

### Quick Wins (high impact, low effort, low risk)

| ID | Fix | Effort |
|----|-----|--------|
| P1-01 | `strings.EqualFold` in MapVMStatus (fix M6 regression) | Minutes |
| P2-02 | `sort.Slice` with total-order composite key | Minutes |
| P2-04 | Prune `lastPublish` on resource delete | Minutes |
| P2-06 | Remove duplicate chi Logger middleware | Minutes |
| P3-01 | Typed struct for RFC 7807 | Minutes |
| P3-02 | MaxBodySize only on mutating methods | Minutes |
| P3-04 | Size hint on params map | Minutes |

### High-Value Investments

| ID | Fix | Effort |
|----|-----|--------|
| P1-02 | Parallel monitor kweb calls (errgroup) | Hours |
| P2-01 | Fix RequestLogger — store only request_id string | Hours |
| P2-03 | singleflight on health cache | Hours |
| P2-07 | `HasProfile()` without slice copy | Hours |
| P3-05 | Validator skipper for health endpoints | Minutes |

### Strategic (evaluate if fleet grows)

| ID | Fix | Effort |
|----|-----|--------|
| P2-05 | Cache kubeconfig for ACTIVE clusters | Days |
| D7 | Batch monitor store writes | Days |
| D3 | Separate bbolt buckets per type | Days |

### Defer

| ID | Fix | Reason |
|----|-----|--------|
| D2 | `sync.Pool` for `statusWriter` | <10 req/s, negligible GC pressure |
| D6 | Async NATS publisher | Event rate <10/poll, sync Publish is non-blocking |
| D9 | `ListClusters` double-unmarshal | <50 clusters, kweb API format constrains |
