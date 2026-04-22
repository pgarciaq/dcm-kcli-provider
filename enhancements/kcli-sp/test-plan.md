# dcm-kcli-provider TDD Test Plan

**Status:** Implemented — 128 specs across 8 packages, all passing with `-race`

## Summary

| Package | Specs | Focus |
|---|---|---|
| `cmd/dcm-kcli-provider` | 6 | Server lifecycle, self-probe, shutdown, timeouts, lifecycle logs |
| `internal/config` | 6 | Env parsing, required vars, defaults, durations, HTTP timeouts |
| `internal/events` | 11 | NATS publisher, CloudEvents, NoopPublisher, IsConnected, nil safety |
| `internal/handlers/v1alpha1` | 39 | VM/cluster CRUD, health, RFC 7807, pagination, panic recovery, rollback |
| `internal/kweb` | 24 | kweb HTTP client, error normalization, CRUD, health, error mapping |
| `internal/monitor` | 20 | Status polling, debounce, orphan detection, reconciliation, publish failure |
| `internal/registration` | 12 | SPM registration, backoff, 4xx handling, idempotent start, Done channel |
| `internal/store` | 11 | bbolt CRUD, name index, type/status filtering, persistence |
| **Total** | **128** | |

## Test ID Convention

New tests follow the formal `TC-{AREA}-{UT|IT}-{NNN}` convention (aligned with ACM/KubeVirt peer SPs). Legacy tests retain `C-NN` comment IDs for traceability to the original TDD plan.

---

## Phase 1: Configuration (`internal/config`)

| ID | Test | Type |
|---|---|---|
| C-01 | returns error when KWEB_URL is missing | Unit |
| C-02 | returns error when SPM_URL is missing | Unit |
| C-03 | populates all defaults when only required vars are set | Unit |
| C-04 | parses custom duration strings | Unit |
| C-05 | returns error on invalid duration string | Unit |
| TC-CFG-UT-006 | ReadTimeout, WriteTimeout, IdleTimeout defaults are non-zero | Unit |

## Phase 2: State Store (`internal/store`)

| ID | Test | Type |
|---|---|---|
| C-06 | creates a bbolt DB file at the given path | Unit |
| C-07 | persists and retrieves a resource entry | Unit |
| C-08 | returns ErrNotFound for unknown ID | Unit |
| C-09 | lists only entries matching resource type | Unit |
| C-10 | deletes an entry so Get returns ErrNotFound | Unit |
| C-11 | updates only the status field | Unit |
| C-12 | lists entries filtered by type and status | Unit |
| C-13 | persists all fields across close and reopen | Unit |
| C-14 | resolves DCM ID to kcli name | Unit |
| C-15 | finds entry by kcli name via the name index | Unit |
| C-15b | returns ErrNotFound for unknown kcli name | Unit |

## Phase 3: kweb Client (`internal/kweb`)

### Error Normalization

| ID | Test | Type |
|---|---|---|
| C-16 | normalizes JSON error with result/reason fields into KwebError with Reason | Unit |
| C-17 | normalizes plain string error body into KwebError with Reason | Unit |
| C-18 | normalizes empty JSON body {} with HTTP 400 into KwebError with StatusCode | Unit |
| C-19 | returns ErrKwebUnreachable when connection is refused | Unit |
| C-20 | returns ErrKwebUnreachable when kweb hangs past timeout | Unit |

### VM Operations

| ID | Test | Type |
|---|---|---|
| C-21 | sends POST /vms with name and profile in JSON body | Unit |
| C-22 | returns success on HTTP 200 | Unit |
| C-23 | returns ErrConflict on kweb "already exists" error | Unit |
| C-23b | returns ErrConflict when reason contains "conflict" | Unit |
| C-24 | lists VMs from kweb | Unit |
| C-25 | gets a single VM by name | Unit |
| C-26 | sends DELETE /vms/{name} | Unit |
| C-27 | lists profiles from GET /vmprofiles | Unit |

### Cluster Operations

| ID | Test | Type |
|---|---|---|
| C-28 | sends POST /kubes with name and cluster type in JSON body | Unit |
| C-29 | returns immediately from cluster creation | Unit |
| C-30 | lists clusters from GET /kubes | Unit |
| C-31 | gets a single cluster by name | Unit |
| C-32 | sends DELETE /kubes/{name} | Unit |

### Health Probing

| ID | Test | Type |
|---|---|---|
| C-33 | returns healthy when kweb responds 200 to GET /host | Unit |
| C-34 | returns unhealthy on non-200 response | Unit |
| C-35 | returns false and ErrKwebUnreachable when kweb is down | Unit |

### Error Mapping (Peer Parity)

| ID | Test | Type |
|---|---|---|
| TC-KWB-ERR-001 | HTTP 404 from kweb yields KwebError with StatusCode 404 | Unit |
| TC-KWB-ERR-002 | HTTP 500 from kweb yields KwebError with StatusCode 500 | Unit |
| TC-KWB-ERR-003 | HTTP 503 from kweb yields KwebError with StatusCode 503 | Unit |

## Phase 4: Events Publisher (`internal/events`)

| ID | Test | Type |
|---|---|---|
| C-36 | mockPublisher satisfies the Publisher interface including Close() | Unit |
| C-37 | returns error when NATS URL is unreachable | Unit |
| C-38 | publishes VM event with correct subject and fields | Unit |
| C-38b | NATSPublisher builds CloudEvent with source, type, subject for VM events | Unit |
| C-39 | publishes cluster event with correct subject and fields | Unit |
| C-39b | NATSPublisher builds CloudEvent with source, type, subject for cluster events | Unit |
| C-40 | StatusEvent serializes to JSON with id, status, message fields | Unit |
| — | NoopPublisher satisfies Publisher interface | Unit |
| TC-EVT-UT-001 | NoopPublisher IsConnected is true and Publish does not error | Unit |
| TC-EVT-UT-002 | disconnected NATSPublisher returns NotConnectedError for PublishVMEvent | Unit |
| TC-EVT-UT-003 | NATSPublisher after Close returns NotConnectedError on publish | Unit (skip if no NATS) |

## Phase 5: Status Monitor (`internal/monitor`)

### Core Polling

| ID | Test | Type |
|---|---|---|
| C-41 | calls kweb ListVMs and ListClusters on each poll | Unit |
| C-42 | detects VM status change and publishes RUNNING event | Unit |
| C-43 | debounces rapid status changes and publishes the final state | Unit |
| C-52 | refreshes profile cache on each poll | Unit |
| C-53 | stops cleanly when context is cancelled | Unit |

### VM Status Mapping

| ID | Test | Type |
|---|---|---|
| C-44 | maps kweb down + recently created to PROVISIONING | Unit |
| C-45 | maps kweb down + old timestamp to STOPPED | Unit |
| C-46 | maps kweb paused to PAUSED | Unit |
| C-47 | maps kweb error/crashed/nostate to ERROR | Unit |
| C-48 | maps kweb shuttingdown to STOPPING | Unit |

### Deletion and Orphan Detection

| ID | Test | Type |
|---|---|---|
| C-49 | publishes DELETED and removes from store when VM is missing | Unit |
| C-50 | transitions cluster to ERROR after creation timeout | Unit |
| C-51 | detects orphan resources with dcm- prefix not in store via PollOnce | Unit |
| C-51b | detects orphans on each tick via Run() | Unit |

### Reconciliation (Restart Recovery)

| ID | Test | Type |
|---|---|---|
| C-85 | marks store entries as DELETED when resources are gone from kweb | Unit |
| C-86 | detects orphans: dcm- prefixed resources in kweb but not in store | Unit |
| C-87 | handles empty store (store loss): logs orphans for all dcm- resources | Unit |

### Publish Failure Resilience (Peer Parity)

| ID | Test | Type |
|---|---|---|
| TC-MON-UT-001 | continues polling after publish failure and updates store status | Unit |
| TC-MON-UT-002 | poll loop continues after publish errors under Run() | Unit |
| TC-MON-UT-009 | does not publish when status is unchanged | Unit |

## Phase 6: HTTP Handlers (`internal/handlers/v1alpha1`)

### Health

| ID | Test | Type |
|---|---|---|
| C-54 | returns 200 with pass, version, and uptime when kweb is healthy | Unit |
| C-55 | returns 503 with fail status and message when kweb is unhealthy | Unit |
| TC-HLT-UT-007 | uptime increases across successive health checks | Unit |

### VM Create

| ID | Test | Type |
|---|---|---|
| C-56 | creates a VM and returns 201 with PROVISIONING status | Unit |
| C-57 | stores entry in bbolt with prefixed kcli name | Unit |
| C-58 | returns 400 with full RFC 7807 body when memory.size is missing | Unit |
| C-59 | returns 400 when profile is not found | Unit |
| C-60 | returns 409 when VM already exists | Unit |
| C-61 | returns 502 when kweb is unreachable | Unit |
| TC-HDL-CRT-UT-020 | VM creation rolls back kweb when store.Put fails | Unit |

### VM List

| ID | Test | Type |
|---|---|---|
| C-62 | returns only DCM-managed VMs, not all kweb VMs | Unit |
| C-63 | paginates VM list with max_page_size and next_page_token | Unit |
| TC-HDL-LST-UT-003 | max_page_size=0 uses default of 50 | Unit |
| TC-HDL-LST-UT-004 | invalid page_token is treated as start | Unit |
| TC-HDL-LST-UT-005 | empty VM list returns empty results array | Unit |
| TC-HDL-LST-UT-006 | max_page_size larger than total returns all VMs | Unit |
| TC-HDL-LST-UT-007 | VM list returns all entries including empty KcliName without error | Unit |

### VM Get / Delete

| ID | Test | Type |
|---|---|---|
| C-64 | returns VM with user-facing name (not prefixed) | Unit |
| C-65 | returns 404 for unknown VM ID | Unit |
| C-66 | deletes VM, publishes DELETED event, removes from store | Unit |
| C-67 | returns 404 when deleting unknown VM | Unit |
| TC-HDL-GET-UT-010 | returns 200 with DELETING status for VM in store | Unit |
| TC-HDL-DEL-UT-008 | returns 204 when deleting a VM already in DELETING state without calling kweb | Unit |

### Cluster Create

| ID | Test | Type |
|---|---|---|
| C-68 | creates a k3s cluster and returns 201 with CREATING status | Unit |
| C-69 | stores cluster entry with prefixed name | Unit |
| C-70 | rejects "kind" cluster type with 400 | Unit |
| C-71 | rejects unsupported cluster type with 400 | Unit |
| TC-HDL-CRT-UT-021 | cluster creation rolls back kweb when store.Put fails | Unit |

### Cluster List / Get / Delete

| ID | Test | Type |
|---|---|---|
| C-72 | returns only DCM-managed clusters, not external kweb clusters | Unit |
| C-73 | returns cluster with user-facing name | Unit |
| C-74 | returns 404 with RFC 7807 for unknown cluster ID | Unit |
| C-75 | deletes cluster, publishes DELETED event, removes from store | Unit |
| TC-HDL-GET-UT-011 | returns 200 with DELETING status for cluster in store | Unit |
| TC-HDL-DEL-UT-009 | returns 204 when deleting a cluster already in DELETING state without calling kweb | Unit |

### HTTP Hardening (Peer Parity)

| ID | Test | Type |
|---|---|---|
| TC-HTTP-UT-003 | returns RFC 7807 500 when handler panics | Unit |
| TC-HTTP-UT-004 | returns 400 RFC 7807 for malformed JSON on POST /vms | Unit |
| TC-HTTP-UT-005 | returns 400 RFC 7807 for malformed JSON on POST /clusters | Unit |
| TC-HDL-POST-UT-018 | returns 400 RFC 7807 with invalid JSON body for empty POST /vms | Unit |
| TC-HDL-POST-UT-019 | returns 400 RFC 7807 with invalid JSON body for empty POST /clusters | Unit |

## Phase 7: Registration (`internal/registration`)

| ID | Test | Type |
|---|---|---|
| C-76 | sends POST to /providers with VM registration payload | Unit |
| C-77 | sends POST with cluster registration payload | Unit |
| C-78 | retries with exponential backoff on server error | Unit |
| C-79 | stops retrying when context is cancelled during backoff | Unit |
| TC-REG-UT-001 | SPM 400 causes immediate registration failure without retry | Unit |
| TC-REG-UT-002 | SPM 409 causes immediate registration failure without retry | Unit |
| TC-REG-UT-003 | calling StartBackground twice only performs registration once | Unit |
| TC-REG-UT-004 | Done channel closes when background registration succeeds | Unit |
| TC-REG-UT-005 | Done channel closes when background registration fails | Unit |
| TC-REG-UT-006 | Done channel closes when context is cancelled during 500 retries | Unit |
| TC-REG-IT-001 | HTTP server serves requests while background registration retries | Integration |
| TC-REG-IT-002 | HTTP server still serves after registration fails when context is cancelled | Integration |

## Phase 8: Server Lifecycle (`cmd/dcm-kcli-provider`)

| ID | Test | Type |
|---|---|---|
| C-80 | starts, self-probes /health, then registration begins | Integration |
| C-81 | fetches and logs available VM profiles on startup | Integration |
| C-82 | shuts down gracefully: HTTP drains, NATS closes, bbolt closes | Integration |
| C-83 | logs warning when shutdown timeout is exceeded | Integration |
| C-84 | returns 504 when handler exceeds request timeout | Integration |
| TC-LIFE-UT-001 | logs HTTP server listening, self-probe succeeded, shutdown signal, and graceful shutdown complete | Integration |

---

## Peer SP Parity Coverage

The following gaps were identified by comparing against the ACM cluster SP (~200 tests) and KubeVirt VM SP (~75 tests), and are now covered:

| Gap | Description | Test IDs |
|---|---|---|
| 1 | No retry on 4xx | TC-REG-UT-001, TC-REG-UT-002 |
| 2 | Idempotent Start() | TC-REG-UT-003 |
| 3 | SP serves despite registration failure | TC-REG-IT-001, TC-REG-IT-002 |
| 4 | NATS down does not block startup | Production fix (NoopPublisher fallback) |
| 5 | Publish failure doesn't crash poll loop | TC-MON-UT-001, TC-MON-UT-002 |
| 6 | Panic recovery → RFC 7807 | TC-HTTP-UT-003 |
| 7 | Malformed JSON → RFC 7807 | TC-HTTP-UT-004, TC-HTTP-UT-005 |
| 8 | Delete idempotency (DELETING→204) | TC-HDL-DEL-UT-008, TC-HDL-DEL-UT-009 |
| 9 | GET during delete shows DELETING | TC-HDL-GET-UT-010, TC-HDL-GET-UT-011 |
| 10 | Uptime monotonicity | TC-HLT-UT-007 |
| 11 | IsConnected / NotConnectedError | TC-EVT-UT-001, TC-EVT-UT-002, TC-EVT-UT-003 |
| 12 | No publish when status unchanged | TC-MON-UT-009 |
| 13 | Done() channel on registration | TC-REG-UT-004, TC-REG-UT-005, TC-REG-UT-006 |
| 14 | Formal TC-xxx IDs | All new tests use TC- convention |
| 15 | HTTP server timeouts configured | TC-CFG-UT-006 |
| 16 | Request logging middleware | Production (Chi middleware.Logger) |
| 17 | Startup/shutdown lifecycle logs | TC-LIFE-UT-001 |
| 18 | Empty body POST → 400 | TC-HDL-POST-UT-018, TC-HDL-POST-UT-019 |
| 19 | Skip failed conversions in list | TC-HDL-LST-UT-007 |
| 20 | Rollback on store.Put failure | TC-HDL-CRT-UT-020, TC-HDL-CRT-UT-021 |
| 21 | Comprehensive kweb error mapping | TC-KWB-ERR-001, TC-KWB-ERR-002, TC-KWB-ERR-003 |
| 22 | Pagination edge cases | TC-HDL-LST-UT-003 through TC-HDL-LST-UT-006 |
