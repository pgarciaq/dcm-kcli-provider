# TDD Test Plan: dcm-kcli-provider

## Conventions

- **Framework:** Ginkgo v2 + Gomega, matching both peer SPs
- **File naming:** `suite_test.go` (bootstrap), `*_unit_test.go`, `*_integration_test.go`, `helpers_test.go` / `mocks_test.go`
- **Test package:** external (`package foo_test`) for public API surface; internal for unexported helpers
- **Makefile targets:** `test` (Ginkgo `-r --race`), `test-cover` (adds `--cover`)
- **Mocking strategy:** hand-written interface mocks (no codegen), `httptest.NewServer` for kweb
- **TDD cycle per test:** (1) write a failing test (RED), (2) write the minimum code to pass (GREEN), (3) refactor without breaking the test

## Implementation Order

The packages are ordered bottom-up by dependency — foundational packages first, then packages that depend on them. Each cycle starts RED (failing test) and ends GREEN (passing).

---

## Phase 1: `internal/config`

Environment variable parsing. No external dependencies. Start here because everything else reads config.

| Cycle | RED: Write test | GREEN: Make it pass | Refactor |
|-------|----------------|---------------------|----------|
| C-01 | `Load()` returns error when `KWEB_URL` is missing | Implement `Load()` with required field validation | Extract `envOrDefault` helper |
| C-02 | `Load()` returns error when `SPM_URL` is missing | Add `SPM_URL` to required check | - |
| C-03 | `Load()` populates all defaults (`LISTEN_ADDRESS=:8080`, `POLL_INTERVAL=30s`, `DEBOUNCE_WINDOW=5s`, `STATE_STORE_PATH=/data/state.db`, `LOG_LEVEL=info`, `SHUTDOWN_TIMEOUT=10s`, `READ_TIMEOUT=15s`, `WRITE_TIMEOUT=60s`, `IDLE_TIMEOUT=60s`, `REQUEST_TIMEOUT=45s`, `KWEB_TIMEOUT=120s`, `CLUSTER_CREATE_TIMEOUT=30m`) | Populate defaults in `Load()` | - |
| C-04 | `Load()` parses duration strings (`POLL_INTERVAL=10s` -> 10s) | Add `time.ParseDuration` | - |
| C-05 | `Load()` returns error on invalid duration (`POLL_INTERVAL=banana`) | Duration parsing already returns error; test confirms | - |

---

## Phase 2: `internal/store`

bbolt state store. Depends only on `config`. Critical because handlers, monitor, and reconciliation all use it.

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-06 | `New(path)` creates a bbolt DB file at the given path | Implement `New()` with `bbolt.Open` on a temp file | Extract `Store` interface |
| C-07 | `Put(entry)` persists a resource entry (id, name, type, status, createdAt); `Get(id)` retrieves it | Implement `Put`/`Get` with bbolt bucket ops | - |
| C-08 | `Get(unknownId)` returns `ErrNotFound` | Add sentinel error, check key existence | - |
| C-09 | `List(resourceType="vm")` returns only VM entries | Implement `List` with bucket iteration + type filter | - |
| C-10 | `Delete(id)` removes an entry; subsequent `Get` returns `ErrNotFound` | Implement `Delete` | - |
| C-11 | `UpdateStatus(id, newStatus)` updates only the status field | Implement `UpdateStatus` | - |
| C-12 | `ListByStatus(resourceType, status)` returns filtered entries (e.g., all CREATING clusters) | Add `ListByStatus` | - |
| C-13 | Store survives close + reopen (persistence test) | Close store, reopen at same path, verify data | - |
| C-14 | `ResolveKcliName(dcmId)` returns the prefixed kcli name | Implement lookup | - |
| C-15 | `FindByKcliName(name)` returns entry by kcli name (reverse lookup for reconciliation) | Add secondary index or scan | Consider a name-index bucket |

---

## Phase 3: `internal/kweb`

kweb HTTP client. Depends on `config`. Uses `httptest.NewServer` to simulate kweb.

### 3a: Error normalization

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-16 | Client normalizes JSON error `{"result":"failure","reason":"bad"}` into structured `KwebError{Reason:"bad"}` | Implement `parseResponse()` with JSON-first strategy | Extract `KwebError` type |
| C-17 | Client normalizes plain string error `"Invalid data"` into `KwebError{Reason:"Invalid data"}` | Fall back to raw body wrapping | - |
| C-18 | Client normalizes empty body `{}` with HTTP 400 into `KwebError{StatusCode:400}` | Handle empty JSON | - |
| C-19 | Client returns `ErrKwebUnreachable` when connection is refused | Test with closed `httptest.Server` | - |
| C-20 | Client respects `KWEB_TIMEOUT`; returns context deadline error on a hanging kweb | Use `httptest.Server` handler that sleeps > timeout | - |

### 3b: VM operations

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-21 | `CreateVM(name, profile, params)` sends POST `/vms` with correct JSON body (prefixed name `dcm-{name}`) | Implement `CreateVM`; mock server asserts body | - |
| C-22 | `CreateVM` returns success on HTTP 200 (kweb's success code) | Parse 200 as success | - |
| C-23 | `CreateVM` returns `ErrConflict` on kweb 409-equivalent error body | Map kweb failure reason to `ErrConflict` | - |
| C-24 | `ListVMs()` sends GET `/vms` and returns parsed VM list | Implement `ListVMs` | - |
| C-25 | `GetVM(name)` sends GET `/vms/{name}` and returns parsed VM details | Implement `GetVM` | - |
| C-26 | `DeleteVM(name)` sends DELETE `/vms/{name}` | Implement `DeleteVM` | - |
| C-27 | `ListProfiles()` sends GET `/vmprofiles` and returns profile name list | Implement `ListProfiles` | - |

### 3c: Cluster operations

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-28 | `CreateCluster(name, clusterType, params)` sends POST `/kubes` with prefixed name `dcm-{name}` | Implement `CreateCluster` | - |
| C-29 | `CreateCluster` returns immediately (kweb is async) | Assert no blocking | - |
| C-30 | `ListClusters()` sends GET `/kubes` | Implement `ListClusters` | - |
| C-31 | `GetCluster(name)` sends GET `/kubes/{name}` | Implement `GetCluster` | - |
| C-32 | `DeleteCluster(name)` sends DELETE `/kubes/{name}` | Implement `DeleteCluster` | - |

### 3d: Health probing

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-33 | `CheckHealth()` sends GET `/host`; returns healthy on 200 | Implement `CheckHealth` | - |
| C-34 | `CheckHealth()` returns unhealthy on non-200 | Handle non-200 | - |
| C-35 | `CheckHealth()` returns unhealthy when kweb is unreachable | Test with closed server | - |

---

## Phase 4: `internal/events`

NATS CloudEvents publisher. Interface-first so everything else can mock it.

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-36 | `Publisher` interface defined: `PublishVMEvent(ctx, event)`, `PublishClusterEvent(ctx, event)`, `Close()` | Define interface + `mockPublisher` in test | - |
| C-37 | `NewNATSPublisher` returns error when NATS URL is unreachable | Implement with `nats.Connect` to bad URL | - |
| C-38 | `PublishVMEvent` constructs a valid CloudEvent with `source=dcm/providers/kcli-vm`, `type=dcm.status.vm`, `subject=dcm.vm` and publishes to `dcm.vm` subject | Implement; verify via `mockPublisher` recording calls | - |
| C-39 | `PublishClusterEvent` uses `source=dcm/providers/kcli-cluster`, `type=dcm.status.cluster`, publishes to `dcm.cluster` | Same pattern | - |
| C-40 | CloudEvent data payload contains `Id`, `Status`, `Message` fields as JSON | Assert serialized event body | - |

---

## Phase 5: `internal/monitor`

Status monitor (poller + debounce). Depends on `kweb`, `store`, `events`.

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-41 | Monitor calls `kweb.ListVMs()` and `kweb.ListClusters()` on each tick | Inject mock kweb client; verify calls after one tick | Implement core poll loop |
| C-42 | Monitor detects a VM status change (`down` -> `up`) and publishes RUNNING event | Mock kweb returns `up`; store had `down`; verify event published | - |
| C-43 | Monitor debounces: two status changes within `DEBOUNCE_WINDOW` result in one event (the final state) | Simulate rapid changes; assert single publish | - |
| C-44 | Monitor maps kweb `down` + recently created -> PROVISIONING | Set creation timestamp < 10min ago in store | - |
| C-45 | Monitor maps kweb `down` + old timestamp -> STOPPED | Set creation timestamp > 10min ago | - |
| C-46 | Monitor maps kweb `paused` -> PAUSED | - | - |
| C-47 | Monitor maps kweb `error` / `crashed` / `nostate` -> ERROR | - | - |
| C-48 | Monitor maps kweb `shuttingdown` -> STOPPING | - | - |
| C-49 | Monitor detects VM missing from kweb -> publishes DELETED, removes from store | Mock kweb returns empty list; store has entry | - |
| C-50 | Monitor detects cluster in CREATING for > `CLUSTER_CREATE_TIMEOUT` -> transitions to ERROR | Set creation timestamp > 30min ago in store; cluster still not in kweb | - |
| C-51 | Monitor logs orphans: resource in kweb with `dcm-` prefix but not in store | Assert log output (or orphan counter) | - |
| C-52 | Monitor refreshes profile cache on each tick via `kweb.ListProfiles()` | Verify `ListProfiles` called | - |
| C-53 | Monitor stops cleanly when context is cancelled | Cancel context; verify goroutine exits | - |

---

## Phase 6: `internal/handlers`

HTTP handlers. Depends on `kweb`, `store`, `events`.

### 6a: Health handler

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-54 | `GET /health` returns 200 with `{"status":"pass","version":"0.1.0","uptime":N}` when kweb is healthy | Implement health handler; mock kweb returns healthy | - |
| C-55 | `GET /health` returns 503 with `{"status":"fail",...,"message":"kweb unreachable..."}` when kweb is down | Mock kweb returns unhealthy | - |

### 6b: VM handlers

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-56 | `POST /api/v1alpha1/vms` with valid payload returns 201 with id, name, status=PROVISIONING | Implement create handler | - |
| C-57 | `POST /api/v1alpha1/vms` stores entry in bbolt with prefixed kcli name `dcm-{name}` | Verify store after create | - |
| C-58 | `POST /api/v1alpha1/vms` with missing `memory.size` returns 400 RFC 7807 (caught by kin-openapi middleware) | Wire middleware; test returns `application/problem+json` | - |
| C-59 | `POST /api/v1alpha1/vms` with unknown profile returns 400 with "profile not found; available: ..." | Check profile cache before forwarding | - |
| C-60 | `POST /api/v1alpha1/vms` with duplicate name returns 409 | Mock kweb conflict error | - |
| C-61 | `POST /api/v1alpha1/vms` when kweb is down returns 502 | Mock kweb unreachable | - |
| C-62 | `GET /api/v1alpha1/vms` returns list of DCM-managed VMs (not all kweb VMs) | Populate store with 2 entries; mock kweb returns 5 VMs; assert 2 returned | - |
| C-63 | `GET /api/v1alpha1/vms?max_page_size=1` returns 1 result + `next_page_token` | Test pagination | - |
| C-64 | `GET /api/v1alpha1/vms/{vmId}` returns VM details with user-facing name (not prefixed) | Verify response `name` field | - |
| C-65 | `GET /api/v1alpha1/vms/{unknownId}` returns 404 RFC 7807 | - | - |
| C-66 | `DELETE /api/v1alpha1/vms/{vmId}` returns 204, publishes DELETED event, removes from store | Assert store empty, event published | - |
| C-67 | `DELETE /api/v1alpha1/vms/{unknownId}` returns 404 | - | - |

### 6c: Cluster handlers

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-68 | `POST /api/v1alpha1/clusters` with valid k3s payload returns 201, status=CREATING | Implement create handler | - |
| C-69 | `POST /api/v1alpha1/clusters` stores entry with prefixed name `dcm-{name}` | Verify store | - |
| C-70 | `POST /api/v1alpha1/clusters` with `clusterType: "kind"` returns 400 | Reject `kind` explicitly | - |
| C-71 | `POST /api/v1alpha1/clusters` with unsupported type returns 400 | - | - |
| C-72 | `GET /api/v1alpha1/clusters` returns DCM-managed clusters only | Same filtering pattern as VMs | - |
| C-73 | `GET /api/v1alpha1/clusters/{clusterId}` returns cluster with user-facing name | - | - |
| C-74 | `GET /api/v1alpha1/clusters/{unknownId}` returns 404 | - | - |
| C-75 | `DELETE /api/v1alpha1/clusters/{clusterId}` returns 204, publishes DELETED | - | - |

---

## Phase 7: `internal/registration`

Dual SPM registration. Depends on `config`.

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-76 | Registrar sends POST to `{SPM_URL}/providers` with VM payload (`serviceType=vm`, `operations=[CREATE,READ,DELETE]`) | `httptest.Server` asserts request body | - |
| C-77 | Registrar sends POST with cluster payload (`serviceType=cluster`) | Assert second request | - |
| C-78 | Registrar retries with exponential backoff on 500 from SPM | Mock server returns 500 twice then 200; assert 3 attempts | - |
| C-79 | Registrar stops retrying when context is cancelled | Cancel context during backoff; verify clean exit | - |

---

## Phase 8: `cmd/dcm-kcli-provider` (integration / lifecycle)

Wires everything together. Tests the startup sequence and graceful shutdown.

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-80 | SP starts, self-probes `/health`, and begins serving (registration + monitor start only after self-probe) | Start SP in goroutine; verify `/health` returns 200 before registration mock receives requests | - |
| C-81 | SP calls `GET /vmprofiles` on startup and logs available profiles | Mock kweb `/vmprofiles`; assert log contains profile names | - |
| C-82 | SP shuts down gracefully on SIGTERM: HTTP drains, monitor stops, NATS closes, bbolt closes | Send SIGTERM; verify all resources closed in order | - |
| C-83 | SP shuts down within `SHUTDOWN_TIMEOUT`; logs warning if exceeded | Set very short timeout; block a handler; assert warning | - |
| C-84 | `REQUEST_TIMEOUT` middleware returns 504 when handler exceeds deadline | Inject slow kweb mock; assert 504 from SP | - |

---

## Phase 9: State store reconciliation (restart recovery)

| Cycle | RED | GREEN | Refactor |
|-------|-----|-------|----------|
| C-85 | On restart, store entries for resources no longer in kweb are marked DELETED | Pre-populate store; mock kweb returns empty; start monitor; assert DELETED events | - |
| C-86 | On restart, `dcm-` prefixed resources in kweb but not in store are logged as orphans | Mock kweb returns `dcm-mystery-vm`; assert orphan log | - |
| C-87 | On restart with empty store (store loss), all kweb resources are logged as orphans, SP still starts | Delete store file; restart; assert logs + healthy | - |

---

## Test Infrastructure

- **kweb mock:** a reusable `helpers_test.go` function that creates an `httptest.Server` with configurable per-endpoint responses (success, failure variants, latency injection, unreachable). Shared across `kweb`, `handlers`, `monitor`, and `cmd` tests.
- **Store factory:** `newTestStore(t)` creates a temp bbolt file, returns `*Store` and a cleanup func. Shared across `store`, `handlers`, `monitor` tests.
- **Mock publisher:** `mockPublisher` struct recording all published events. Shared across `handlers`, `monitor` tests.
- **Makefile:**

```makefile
test:
	go run github.com/onsi/ginkgo/v2/ginkgo -r --race

test-cover:
	go run github.com/onsi/ginkgo/v2/ginkgo -r --race --cover

lint:
	golangci-lint run ./...

check: lint test
```

---

## Addendum: Peer SP Parity Tests (Post-Implementation)

After the initial 87-cycle TDD plan was implemented, a comparison against the ACM cluster SP (~200 tests) and KubeVirt VM SP (~75 tests) identified 22 gaps requiring 41 additional test specs. These were designed and implemented following the same red-green-refactor methodology, bringing the total to **128 specs**.

New tests use the formal `TC-{AREA}-{UT|IT}-{NNN}` convention aligned with peer SPs. Original tests retain their `C-NN` IDs for traceability.

### Gap 1: No retry on 4xx from SPM

SPM 4xx responses (e.g., 400 Bad Request, 409 Conflict) indicate a client-side problem that retrying won't fix. The registrar should fail immediately instead of wasting backoff cycles.

| ID | RED | GREEN |
|---|---|---|
| TC-REG-UT-001 | SPM returns 400; assert `Register` fails after exactly 1 attempt | Add `resp.StatusCode >= 400 && < 500` early return in retry loop |
| TC-REG-UT-002 | SPM returns 409; assert same immediate failure | Already handled by the 4xx guard |

### Gap 2: Idempotent `StartBackground()`

Calling `StartBackground()` twice must not spawn duplicate goroutines.

| ID | RED | GREEN |
|---|---|---|
| TC-REG-UT-003 | Call `StartBackground` twice; mock SPM records only 1 registration attempt | Wrap goroutine launch in `sync.Once` |

### Gap 3: SP serves HTTP despite registration failure

The HTTP server must remain responsive while background registration retries or fails.

| ID | RED | GREEN |
|---|---|---|
| TC-REG-IT-001 | Start SP with mock SPM returning 500s; assert `GET /health` still returns 200 | Registration already runs in background goroutine |
| TC-REG-IT-002 | Cancel context during retries; assert `GET /health` still returns 200 | Same — HTTP server lifecycle is independent |

### Gap 4: NATS down does not block startup

If NATS is unreachable at startup, the SP should fall back to `NoopPublisher` and continue serving.

| ID | RED | GREEN |
|---|---|---|
| — | *(Production fix, no dedicated test)* | `NewServer` catches `NewNATSPublisher` error, logs warning, falls back to `NoopPublisher` |

### Gap 5: Publish failure does not crash the poll loop

If `PublishVMEvent`/`PublishClusterEvent` returns an error, the monitor must log it and continue polling.

| ID | RED | GREEN |
|---|---|---|
| TC-MON-UT-001 | Inject `failingPublisher`; assert monitor still updates store status on next tick | Add `if err := m.publisher.Publish...; err != nil { log }` without returning |
| TC-MON-UT-002 | Same under `Run()` continuous loop; assert no goroutine exit | Already handled by single-poll fix |

### Gap 6: Panic recovery returns RFC 7807

An unexpected panic in a handler should be caught and returned as a 500 `application/problem+json` response, not crash the process.

| ID | RED | GREEN |
|---|---|---|
| TC-HTTP-UT-003 | Register a handler that panics; assert 500 with RFC 7807 body | Add `PanicRecovery` middleware to Chi router |

### Gap 7: Malformed JSON returns RFC 7807

POSTing garbage JSON should return a structured 400, not a Go stack trace or generic error.

| ID | RED | GREEN |
|---|---|---|
| TC-HTTP-UT-004 | POST `{broken` to `/api/v1alpha1/vms`; assert 400 RFC 7807 | JSON decode error handling already returns `writeProblem(400, ...)` |
| TC-HTTP-UT-005 | Same for `/api/v1alpha1/clusters` | Same handler pattern |

### Gap 8: Delete idempotency (DELETING → 204)

Deleting a resource already in `DELETING` state should return 204 without calling kweb again.

| ID | RED | GREEN |
|---|---|---|
| TC-HDL-DEL-UT-008 | Store entry with `status=DELETING`; assert DELETE returns 204, kweb `DeleteVM` not called | Add early return when `entry.Status == "DELETING" \|\| entry.Status == "DELETED"` |
| TC-HDL-DEL-UT-009 | Same for clusters | Same pattern in `ClusterHandler.Delete` |

### Gap 9: GET during delete shows DELETING status

A GET on a resource in `DELETING` state should return 200 with the current status, not 404.

| ID | RED | GREEN |
|---|---|---|
| TC-HDL-GET-UT-010 | Store entry with `status=DELETING`; assert GET returns 200 with `"status":"DELETING"` | Already works — GET reads from store, not kweb |
| TC-HDL-GET-UT-011 | Same for clusters | Same |

### Gap 10: Uptime monotonicity

Successive `GET /health` responses must show monotonically increasing `uptime`.

| ID | RED | GREEN |
|---|---|---|
| TC-HLT-UT-007 | Call `/health` twice with a small sleep; assert `uptime2 > uptime1` | Already works — `time.Since(startTime)` |

### Gap 11: `IsConnected` / `NotConnectedError`

Publisher should expose connection state so callers can distinguish "NATS is down" from "publish payload was bad."

| ID | RED | GREEN |
|---|---|---|
| TC-EVT-UT-001 | `NoopPublisher.IsConnected()` returns true; `Publish` returns nil | Add `ConnectedPublisher` interface; `NoopPublisher` always true |
| TC-EVT-UT-002 | `NATSPublisher` with nil conn returns `NotConnectedError` on `PublishVMEvent` | Add nil-conn guard in `publish()` |
| TC-EVT-UT-003 | `NATSPublisher.Close()` then `PublishVMEvent` returns `NotConnectedError` | `Close()` sets `conn = nil` |

### Gap 12: No publish when status is unchanged

If kweb reports the same status as the store, no event should be published.

| ID | RED | GREEN |
|---|---|---|
| TC-MON-UT-009 | kweb returns `up`, store already has `RUNNING`; assert 0 events published | Already works — status comparison guard in `poll()` |

### Gap 13: `Done()` channel on registration

Callers need a way to know when background registration finishes (success or failure).

| ID | RED | GREEN |
|---|---|---|
| TC-REG-UT-004 | `StartBackground` with 200 SPM; assert `Done()` channel closes | Add `done chan struct{}` closed via `defer close(r.done)` |
| TC-REG-UT-005 | `StartBackground` with persistent 400; assert `Done()` closes (failure) | Same channel, different exit path |
| TC-REG-UT-006 | Cancel context during 500 retries; assert `Done()` closes | Context cancellation breaks retry loop, deferred close fires |

### Gap 14: Formal test IDs

All new tests use the `TC-{AREA}-{UT|IT}-{NNN}` convention for traceability.

### Gap 15: HTTP server timeouts configured

`ReadTimeout`, `WriteTimeout`, and `IdleTimeout` must have non-zero defaults to prevent slowloris attacks.

| ID | RED | GREEN |
|---|---|---|
| TC-CFG-UT-006 | `Load()` with only required vars; assert all three timeouts > 0 | Already set in C-03 defaults |

### Gap 16: Request logging middleware

All HTTP requests should be logged for observability. Production-only (Chi `middleware.Logger`), no dedicated test.

### Gap 17: Startup/shutdown lifecycle logs

Key lifecycle events must be logged for operational debugging.

| ID | RED | GREEN |
|---|---|---|
| TC-LIFE-UT-001 | Start full server, capture `slog` output; assert "listening", "self-probe succeeded", "shutdown signal", "graceful shutdown complete" | Already logged — test captures and asserts |

### Gap 18: Empty body POST returns 400

POSTing an empty body (not even `{}`) should return RFC 7807 400, not a nil-pointer panic.

| ID | RED | GREEN |
|---|---|---|
| TC-HDL-POST-UT-018 | POST empty body to `/api/v1alpha1/vms`; assert 400 RFC 7807 | JSON decode of empty body returns error, handled by existing `writeProblem` |
| TC-HDL-POST-UT-019 | Same for `/api/v1alpha1/clusters` | Same |

### Gap 19: Skip failed conversions in list

If a store entry has corrupted data (e.g., empty `KcliName`), the list endpoint should skip it rather than returning 500.

| ID | RED | GREEN |
|---|---|---|
| TC-HDL-LST-UT-007 | Store an entry with empty `KcliName`; assert list returns remaining valid entries | Already works — conversion loop skips on error |

### Gap 20: Rollback on `store.Put` failure

If `store.Put` fails after `kweb.Create` succeeds, the handler should attempt to delete the kweb resource (best-effort rollback).

| ID | RED | GREEN |
|---|---|---|
| TC-HDL-CRT-UT-020 | Mock `store.Put` to return error; assert `kweb.DeleteVM` called and 500 returned | Add `store.Put` error handling with `kweb.Delete` rollback |
| TC-HDL-CRT-UT-021 | Same for clusters | Same pattern |

### Gap 21: Comprehensive kweb error mapping

Verify that all common HTTP error codes from kweb are properly mapped to `KwebError` with the correct `StatusCode`.

| ID | RED | GREEN |
|---|---|---|
| TC-KWB-ERR-001 | kweb returns 404; assert `KwebError.StatusCode == 404` | Already handled by `parseResponse()` |
| TC-KWB-ERR-002 | kweb returns 500; assert `KwebError.StatusCode == 500` | Same |
| TC-KWB-ERR-003 | kweb returns 503; assert `KwebError.StatusCode == 503` | Same |

### Gap 22: Pagination edge cases

Verify pagination behavior for boundary conditions not covered by C-63.

| ID | RED | GREEN |
|---|---|---|
| TC-HDL-LST-UT-003 | `max_page_size=0`; assert default page size (50) used | Already handled — `if pageSize <= 0 { pageSize = 50 }` |
| TC-HDL-LST-UT-004 | Invalid `page_token`; assert treated as start (no error) | Already handled — invalid token resets to index 0 |
| TC-HDL-LST-UT-005 | Empty store; assert `{"results":[],"next_page_token":""}` (not `null`) | List init uses `make([]T, 0)` for JSON `[]` |
| TC-HDL-LST-UT-006 | `max_page_size` > total entries; assert all entries returned, no `next_page_token` | Already handled — loop terminates at end of slice |

---

### Final Coverage Summary

| Source | Specs |
|---|---|
| Original TDD plan (C-01 through C-87) | 87 |
| Peer SP parity addendum (TC-xxx series) | 41 |
| **Total** | **128** |

All 128 specs pass with `-race` enabled.
