# Performance Audit Report: dcm-kcli-provider

## Date and Scope

**Date:** 2026-06-17
**Codebase:** commit `c0d2905` (main)
**Scope:** Full codebase — API handlers, kweb client, bbolt store, monitor loop,
NATS publisher, metrics middleware, registration

> **Status:** All 12 actionable findings (M1–M11, M15) have been implemented.
> 3 findings (M12, M13, M14) deferred as not worth the complexity at homelab scale.

## Overall Assessment

The dcm-kcli-provider is an **I/O-bound** service. Its critical paths are
dominated by network round-trips to kweb (HTTP) and NATS publishes, not by CPU
or memory. At the designed scale (~200 VMs, ~50 clusters), the codebase
performs well. The recent parallel list optimization (PERF-01) addressed the
largest latency bottleneck in the API layer.

**Estimated steady-state resource profile (200 VMs, 50 clusters, 30s poll):**

| Metric | Estimate |
|--------|----------|
| RSS | ~15–25 MB (Go runtime + bbolt mmap + HTTP buffers) |
| CPU | <1% (idle between polls, trivial per-request work) |
| Goroutines | 6 steady-state (main, HTTP, monitor, 2 registrars, signal) |
| Poll I/O | 3 kweb GETs + 2 bbolt scans + 0–N NATS publishes per 30s |
| bbolt file | <1 MB at 250 entries × ~150 bytes/entry |

No P0 (critical) findings. The codebase is well-matched to its homelab scope.

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
   `make([]string, 0, len(profiles))` in profile parsing avoid realloc.
6. **Metrics route pattern** — Uses `chi.RouteContext().RoutePattern()` instead
   of `r.URL.Path` to avoid unbounded Prometheus label cardinality.
7. **Response body limit** — `io.LimitReader(resp.Body, 10<<20)` caps kweb
   response reads at 10 MB.
8. **Orphan deduplication** — `seenOrphans` map prevents repeated logging/counting
   of the same orphan across poll cycles.

## Findings by Priority

### P1 — High

#### M1: NATS publish under monitor mutex

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/monitor.go:216` (`publishWithDebounce` → `flushOne`) and L245–254 (`flushPending`) |
| **Current state** | `flushOne` is called while holding `m.mu`. `flushOne` calls `publisher.PublishVMEvent` / `PublishClusterEvent` which does NATS `conn.Publish`. If NATS is slow or buffering, the mutex is held for the duration, blocking `pollVMs`, `pollClusters`, `refreshProfiles`, and `Profiles()` (used by API). |
| **Proposed fix** | Collect events to flush into a local slice under the lock, release the lock, then publish. |
| **Expected impact** | Reduces worst-case mutex hold from `O(NATS_latency × pending_events)` to `O(len(pending))` (microseconds). Prevents monitor stalls when NATS is slow. |
| **Risk** | Low. Event ordering preserved (same goroutine). Race-free since only the monitor goroutine writes `pending`. |
| **Effort** | S (hours) |

#### M2: Store `List` full-scan JSON deserializes every entry

| Field | Detail |
|-------|--------|
| **Location** | `internal/store/store.go:146–157` (`List`), L161–175 (`ListByStatus`), L238–250 (`ListAll`) |
| **Current state** | `List("vm")` iterates the entire `resources` bucket, calling `json.Unmarshal` on every entry (VMs and clusters alike), then filters by `entry.Type`. At 250 entries × ~150 bytes, this is ~250 `json.Unmarshal` calls for a type filter that discards ~50 of them. |
| **Proposed fix** | **Option A (simple):** Add a `type_` prefix to bbolt keys (e.g., `vm:uuid`, `cluster:uuid`) and use `Cursor.Seek("vm:")` + prefix iteration. **Option B (simpler):** Add separate buckets per resource type. **Option C (cheapest):** Keep current structure but pre-filter on a raw byte check before unmarshalling (e.g., `bytes.Contains(v, []byte(`"type":"vm"`))`). |
| **Expected impact** | Option A/B: eliminates ~50% of unnecessary unmarshals. Option C: eliminates marshal overhead for wrong-type entries. At 250 entries this saves ~125 unmarshals × 2 per monitor cycle + per list API call. |
| **Risk** | Option A/B require a store migration. Option C is fragile if JSON field order changes. |
| **Effort** | Option C: S (hours). Option A/B: M (days, needs migration). |

### P2 — Medium

#### M3: `fmt.Sprintf` in per-entry response construction

| Field | Detail |
|-------|--------|
| **Location** | `internal/api/server/vm_handlers.go:306` (`entryToVM`), `cluster_handlers.go:368` (`entryToCluster`) |
| **Current state** | `fmt.Sprintf("vms/%s", entry.ID)` / `fmt.Sprintf("clusters/%s", entry.ID)` allocates a new string per entry in list responses. At 200 entries, that's 200 `Sprintf` calls per list. |
| **Proposed fix** | Use string concatenation: `"vms/" + entry.ID`. The Go compiler optimizes 2-operand `+` to a single allocation, avoiding `fmt.Sprintf` reflection overhead. |
| **Expected impact** | ~3× faster per-path string construction. Minor in absolute terms (~200µs saved per 200-item list). |
| **Risk** | None. |
| **Effort** | S (minutes) |

#### M4: Maps allocated without size hints in hot paths

| Field | Detail |
|-------|--------|
| **Location** | `vm_handlers.go:159` `make(map[string]kweb.VMInfo)`, `cluster_handlers.go:157` `make(map[string]kweb.ClusterInfo)`, `monitor.go:126` `make(map[string]kweb.VMInfo)`, `monitor.go:164` `make(map[string]kweb.ClusterInfo)` |
| **Current state** | Maps created without capacity hints cause rehashing as entries are added. With 200 VMs, the VM map rehashes ~8 times (0→1→2→4→8→16→32→64→128→256). |
| **Proposed fix** | `make(map[string]kweb.VMInfo, len(kwebVMs))` in all four locations. |
| **Expected impact** | Eliminates map rehashing. Saves ~8 allocations per list/poll at 200 entries. |
| **Risk** | None. |
| **Effort** | S (minutes) |

#### M5: Store `List` slice grows without capacity hint

| Field | Detail |
|-------|--------|
| **Location** | `internal/store/store.go:145,153` (`List`), L162,170 (`ListByStatus`), L239,246 (`ListAll`) |
| **Current state** | `var entries []ResourceEntry` starts at nil; `entries = append(entries, entry)` grows with amortized doubling. For 200 VMs, this causes ~8 grow-and-copy cycles. |
| **Proposed fix** | Cannot pre-size without knowing count (bbolt `ForEach` doesn't expose count). Use `bolt.Bucket.Stats().KeyN` to get key count: `entries := make([]ResourceEntry, 0, b.Stats().KeyN)`. |
| **Expected impact** | Eliminates slice reallocations during full scans. Saves ~8 allocations per list. |
| **Risk** | `Stats().KeyN` counts all keys in the bucket, not just matching type. Over-allocates for `List(type)`, but wastes only memory (no copies). |
| **Effort** | S (minutes) |

#### M6: `strings.ToLower` allocation in VM status mapping

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/status.go:11` (`MapVMStatus`) |
| **Current state** | `strings.ToLower(kwebStatus)` allocates a new string on every call, even when `kwebStatus` is already lowercase (common case from kweb: "up", "down", "running"). Called once per VM per poll cycle. |
| **Proposed fix** | Check if already lowercase first: add fast-path `if kwebStatus == strings.ToLower(kwebStatus)` — no, that still allocates. Better: use a case-insensitive switch via `strings.EqualFold` or simply document that kweb always returns lowercase (it does) and remove the `ToLower`. |
| **Expected impact** | Eliminates 200 string allocations per poll cycle. |
| **Risk** | If kweb ever returns mixed-case status strings, the mapping would miss. Mitigate by adding a default fallback log. |
| **Effort** | S (minutes) |

#### M7: Health check called twice on `/ready` + `/health` scrape

| Field | Detail |
|-------|--------|
| **Location** | `cmd/dcm-kcli-provider/server.go:333` (`readinessHandler`), L382 (`rootHealthHandler`) |
| **Current state** | Both `/ready` and `/health` independently call `s.kwebClient.CheckHealth(ctx)`. If a monitoring system scrapes both endpoints (common pattern), kweb gets 2 health-check HTTP requests per scrape interval instead of 1. |
| **Proposed fix** | Cache the health result for a short TTL (e.g., 5 seconds) using an `atomic.Value` + timestamp. The monitor already polls kweb every 30s; health probes could piggyback on that. |
| **Expected impact** | Halves kweb health-check traffic under dual-probe scraping. Reduces rate-limiter token consumption by 1 req per scrape. |
| **Risk** | Stale health status for up to 5s. Acceptable for liveness/readiness probes. |
| **Effort** | S (hours) |

#### M8: `context.Background()` in NATS publish during shutdown

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/monitor.go:236–238` (`flushOne`) |
| **Current state** | NATS publishes use `context.Background()`, meaning they cannot be cancelled during shutdown. If NATS is unreachable, `flushOne` blocks until the NATS client timeout (default 2s per publish), delaying shutdown by `O(pending_events × 2s)`. |
| **Proposed fix** | Pass the monitor's `ctx` through to `flushOne` and use it for publish calls. The publisher interface already accepts `context.Context` (currently ignored — see M9). |
| **Expected impact** | Prevents shutdown delays when NATS is down. |
| **Risk** | Low. Pending events that couldn't publish are lost (acceptable — they're best-effort status updates). |
| **Effort** | S (minutes) |

#### M9: Publisher ignores context

| Field | Detail |
|-------|--------|
| **Location** | `internal/events/publisher.go:70,81` (`PublishVMEvent`, `PublishClusterEvent`) |
| **Current state** | Both methods accept `context.Context` but ignore it. The NATS `conn.Publish` call is synchronous (writes to buffer, does not block on network). However, if the write buffer is full, it blocks until flushed. No way to cancel. |
| **Proposed fix** | Wrap the publish in a `select` on `ctx.Done()` with the publish in a goroutine, or use `nats.PublishMsg` with a flush timeout. Since `conn.Publish` is typically non-blocking (buffered), this is low priority. |
| **Expected impact** | Enables cancellation of stuck publishes during shutdown. |
| **Risk** | Low. |
| **Effort** | S (hours) |

#### M10: HTTP transport uses default `MaxIdleConnsPerHost` (2)

| Field | Detail |
|-------|--------|
| **Location** | `internal/kweb/client.go:74–76` |
| **Current state** | The kweb HTTP client uses `http.DefaultTransport` which limits idle connections per host to 2. During a poll cycle, the monitor makes 3 sequential requests to the same kweb host. Under concurrent API+monitor load, connection reuse is limited. |
| **Proposed fix** | Set a custom transport: `&http.Transport{MaxIdleConnsPerHost: 10}`. |
| **Expected impact** | Better connection reuse under concurrent load. Eliminates TCP handshake overhead for 3rd+ concurrent request to kweb. |
| **Risk** | None. |
| **Effort** | S (minutes) |

### P3 — Low

#### M11: `parseResponse` deserializes all 2xx bodies to check for `result: failure`

| Field | Detail |
|-------|--------|
| **Location** | `internal/kweb/client.go:362–383` |
| **Current state** | On every successful POST/DELETE response, `parseResponse` reads the full body, deserializes it into `map[string]interface{}`, and checks for `result: "failure"`. This is a kweb quirk (it returns 200 with `{"result":"failure"}` for some errors). |
| **Proposed fix** | Check for the string `"failure"` in the raw bytes first: `if !bytes.Contains(data, []byte("failure")) { return nil }`. Only deserialize if the raw check matches. |
| **Expected impact** | Avoids JSON unmarshal on ~95% of successful responses. Saves one `map[string]interface{}` allocation per create/delete. |
| **Risk** | If the word "failure" appears in a legitimate response field value, it would trigger a false positive — but then the full parse would correctly determine it's not `result: "failure"`. |
| **Effort** | S (minutes) |

#### M12: `statusWriter` heap-escapes on every request

| Field | Detail |
|-------|--------|
| **Location** | `internal/metrics/metrics.go:87` |
| **Current state** | `&statusWriter{ResponseWriter: w, code: http.StatusOK}` escapes to heap because `w` is an interface. This is one allocation per HTTP request. |
| **Proposed fix** | Use `sync.Pool` for `statusWriter` recycling. However, at homelab request rates (<10 req/s), the GC pressure is negligible. |
| **Expected impact** | Eliminates 1 heap allocation per request. Negligible at <10 req/s. |
| **Risk** | `sync.Pool` complexity for minimal gain. |
| **Effort** | S (minutes), but not worth doing at current scale. |

#### M13: `uuid.New().String()` in CloudEvent publish

| Field | Detail |
|-------|--------|
| **Location** | `internal/events/publisher.go:103` |
| **Current state** | Each NATS publish generates a new UUID v4 (`uuid.New().String()` = crypto/rand read + hex encode + string alloc). At homelab scale, this is ~0–10 publishes per poll cycle. |
| **Proposed fix** | Not worth optimizing. UUID generation is ~300ns. Could pre-allocate a pool, but complexity isn't justified. |
| **Expected impact** | Negligible. |
| **Risk** | N/A. |
| **Effort** | N/A — defer. |

#### M14: `json.Marshal`/`json.Unmarshal` for small bbolt entries

| Field | Detail |
|-------|--------|
| **Location** | `internal/store/store.go:118,136,149,166,198,202,243` |
| **Current state** | `ResourceEntry` is a 5-field struct (~150 bytes JSON). Every store read/write calls `json.Marshal` or `json.Unmarshal`. At 250 entries × 2 full scans per poll, that's ~500 unmarshal calls/cycle. |
| **Proposed fix** | **Option A:** Use a binary encoding (e.g., `encoding/gob`, `msgpack`) — ~3× faster for small structs. **Option B:** Use a fixed-size binary format (manual encoding) — ~10× faster but brittle. |
| **Expected impact** | ~1.5ms saved per poll cycle (500 × 3µs per unmarshal). Negligible in absolute terms. |
| **Risk** | Existing data migration needed. Debugging harder without human-readable store format. |
| **Effort** | M (days). Not worth it at current scale. |

#### M15: Orphan detection issues per-VM bbolt transactions

| Field | Detail |
|-------|--------|
| **Location** | `internal/monitor/monitor.go:272` (`detectVMOrphans`) |
| **Current state** | For each `dcm-*` VM in kweb's list, a separate `FindByKcliName` View transaction is opened. With 200 VMs, ~200 of which start with `dcm-`, that's ~200 separate bbolt read transactions. |
| **Proposed fix** | Batch the check: call `store.ListAll()` once (already done in `pollVMs`), build a `map[string]bool` of known kcli names, then iterate kweb VMs and check the map. |
| **Expected impact** | Reduces 200 bbolt transactions to 0 (reuses data already fetched in `pollVMs`). |
| **Risk** | Low. The store data is already available from `pollVMs`. |
| **Effort** | S (hours) |

## Deferred Items — Revisit Triggers

| ID | Item | Revisit When |
|----|------|--------------|
| D1 | Binary encoding for bbolt (M14) | Resource count exceeds 1,000 or poll latency exceeds 500ms |
| D2 | `sync.Pool` for `statusWriter` (M12) | Request rate exceeds 100 req/s sustained |
| D3 | Separate bbolt buckets per type (M2 Option B) | Resource count exceeds 500 or store list latency exceeds 50ms |
| D4 | HTTP/2 to kweb | kweb adds HTTP/2 support |
| D5 | Streaming JSON responses | List response sizes exceed 1 MB |

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
| `kweb.ListClusters` (HTTP GET) | 1 | Network RTT + JSON unmarshal (~50 entries) |
| `store.List("vm")` (bbolt ForEach) | 1 | 250 entries × json.Unmarshal, ~250 filtered to ~200 |
| `store.List("cluster")` (bbolt ForEach) | 1 | 250 entries × json.Unmarshal, ~250 filtered to ~50 |
| `store.FindByKcliName` (bbolt Get) | ~200 | 1 index lookup + 1 data read + 1 unmarshal each |
| `MapVMStatus` | ~200 | `strings.ToLower` + switch per VM |
| `MapClusterStatus` | ~50 | Comparison only |
| `map` creation | 4 | 2 kweb maps (no size hint) + 2 implicit |
| Prometheus observe | 1 | `MonitorPollDuration` histogram |
| Total allocations | ~950 | ~500 json.Unmarshal + ~200 FindByKcliName + maps + strings |

**Per API list request (200 VMs):**

| Operation | Count | Dominant Cost |
|-----------|-------|---------------|
| `store.List("vm")` | 1 (goroutine) | ~250 json.Unmarshal |
| `kweb.ListVMs` | 1 (goroutine, parallel) | Network RTT |
| `sort.Slice` | 1 | O(n log n), n=200 |
| `entryToVM` | ~200 | `fmt.Sprintf` + struct construction |
| Map build | 1 | ~200 insertions, no size hint |
| `statusWriter` | 1 | Heap escape |
| JSON response marshal | 1 | ~200 VM structs |

## ROI Prioritization

### Quick Wins (high impact / low effort / low risk)

| ID | Fix | Effort |
|----|-----|--------|
| M4 | Add size hints to 4 map allocations | Minutes |
| M3 | Replace `fmt.Sprintf` with `+` in `entryToVM`/`entryToCluster` | Minutes |
| M5 | Pre-cap store list slices with `Stats().KeyN` | Minutes |
| M6 | Remove `strings.ToLower` in `MapVMStatus` (kweb returns lowercase) | Minutes |
| M10 | Set `MaxIdleConnsPerHost: 10` on kweb HTTP transport | Minutes |
| M11 | Fast-path `bytes.Contains` check before full `parseResponse` unmarshal | Minutes |

### High-Value Investments

| ID | Fix | Effort |
|----|-----|--------|
| M1 | Extract NATS publish from monitor mutex | Hours |
| M15 | Batch orphan detection using existing store data | Hours |
| M7 | Cache health check result with short TTL | Hours |
| M8 | Pass context through to `flushOne` NATS calls | Minutes |

### Strategic (evaluate carefully)

| ID | Fix | Effort |
|----|-----|--------|
| M2 | Type-prefixed keys or separate buckets in bbolt | Days |
| M9 | Context-aware NATS publish | Hours |

### Defer

| ID | Fix | Reason |
|----|-----|--------|
| M12 | `sync.Pool` for `statusWriter` | <10 req/s, negligible GC pressure |
| M13 | UUID pool for CloudEvents | ~300ns, not worth complexity |
| M14 | Binary bbolt encoding | JSON is fast enough at <500 entries |
