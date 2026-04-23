# E2E Live Testing Results

**Date:** 2026-04-22
**Environment:** Apollo Hypervisor (`hpe-apollo-cn99xx-16.khw.eng.rdu2.dc.redhat.com`)
**Architecture:** aarch64 (ARM64)

## Prerequisites: What You Need Before the SP Does Anything Useful

The kcli SP is just one piece of the DCM stack. By itself, it only exposes a
REST API. For the **full DCM flow** (catalog UI → placement → policy → SP →
backend), the following must be in place:

### 1. Running DCM control plane

All of these must be running (typically via `podman-compose`):

| Component | Purpose |
|-----------|---------|
| API Gateway (Traefik) | Single ingress on `:9080`, routes to all managers |
| Service Provider Manager | Provider registration, health checks, request routing |
| Catalog Manager | Catalog items, catalog item instances, spec resolution |
| Policy Manager | Rego policy evaluation for provider selection |
| Placement Manager | Orchestrates catalog → policy → SPM delegation |
| PostgreSQL | Persistence for catalog-manager and placement-manager |
| NATS JetStream | CloudEvents transport for status updates |

### 2. The kcli SP registered and healthy

The SP must be running with:
- `KWEB_URL` pointing at a running kweb instance
- `SPM_URL` pointing at the SPM endpoint (via gateway or direct)
- `LISTEN_ADDRESS` set to an IP reachable from the container network (e.g.,
  `10.89.0.1:8085` for podman bridge gateway — **not** `localhost`)

After startup, both providers (`kcli-vm`, `kcli-cluster`) must show
`health_status: ready` in SPM.

### 3. Catalog items

Catalog items define what users can provision. Without them, there is nothing
to create through the DCM UI or API.

Example catalog items are provided in this repository:

- [`docs/examples/catalog-item-vm.json`](examples/catalog-item-vm.json) —
  Fedora VM with OS image, memory, and vCPU fields
- [`docs/examples/catalog-item-cluster.json`](examples/catalog-item-cluster.json) —
  K3s cluster with cluster type selector

Create them via the API gateway:

```bash
curl -X POST http://localhost:9080/api/v1alpha1/catalog-items \
  -H "Content-Type: application/json" \
  -d @docs/examples/catalog-item-vm.json

curl -X POST http://localhost:9080/api/v1alpha1/catalog-items \
  -H "Content-Type: application/json" \
  -d @docs/examples/catalog-item-cluster.json
```

### 4. Rego routing policy

The policy manager must have a policy that routes requests to the kcli
providers. Without a routing policy, the placement manager does not know which
provider to delegate to and requests will fail.

An example policy is provided:

- [`docs/examples/policy-route-to-kcli.json`](examples/policy-route-to-kcli.json) —
  unconditionally routes all VM requests to `kcli-vm`

```bash
curl -X POST http://localhost:9080/api/v1alpha1/policies \
  -H "Content-Type: application/json" \
  -d @docs/examples/policy-route-to-kcli.json
```

> **Note:** This example policy is a simple catch-all. In production, use Rego
> logic that inspects `input.spec` fields (labels, regions, capacity) to select
> the appropriate provider. You will also need a separate policy (or a combined
> one) for routing cluster requests to `kcli-cluster`.

### 5. kweb running with images available

kweb must be running and have at least one VM image available (e.g., `fedora41`).
Check available profiles with:

```bash
curl http://localhost:8000/vmprofiles
```

If the profiles list is empty, the SP will still work (kweb validates profiles
on its side), but users will not see available options.

See [`docs/examples/README.md`](examples/README.md) for the full setup
walkthrough including customization options.

---

## Stack Deployed

| Component | Version | Port | Status |
|---|---|---|---|
| Traefik Gateway | v3.4 | 9080 | Healthy |
| Service Provider Manager | main (quay.io) | via gateway | Healthy |
| Catalog Manager | main (quay.io) | via gateway | Healthy |
| Policy Manager | main (quay.io) | via gateway | Healthy |
| Placement Manager | main (quay.io) | via gateway | Healthy |
| PostgreSQL | 16-alpine | 5432 | Healthy |
| NATS JetStream | 2-alpine | 4222, 8222 | Running |
| kweb (kcli v99.0) | 99.0 | 8000 | Running |
| dcm-kcli-provider | 0.1.0 (arm64 binary) | 8085 | Running |

## Test Results Summary

### Registration

| Test | Expected | Actual | Status |
|---|---|---|---|
| SP starts and self-probe succeeds | HTTP 200 on `/api/v1alpha1/health` | 200 | PASS |
| VM provider registers with SPM | 201 Created | 201, provider visible via gateway | PASS |
| Cluster provider registers with SPM | 201 Created | 201, provider visible via gateway | PASS |
| Dual registration (VM + cluster) | Two distinct providers in SPM | Both appear with correct service_type | PASS |
| Providers visible via API gateway | GET /api/v1alpha1/providers | Both providers listed | PASS |
| Collection-level endpoints registered | `/vms` for VM, `/clusters` for cluster | Confirmed via GET /providers | PASS |
| SPM health checks use /vms/health and /clusters/health | health_status: ready | Both show ready | PASS |

### VM Lifecycle

| Test | Expected | Actual | Status |
|---|---|---|---|
| Create VM | 201, status=PROVISIONING | `201 {"id":"...","name":"e2e-final","status":"PROVISIONING"}` | PASS |
| VM transitions to RUNNING | Status changes after kweb provisioning | Status=RUNNING within 10s | PASS |
| List VMs | Returns managed VMs | 200 with `results` array | PASS |
| Get VM by ID | Returns VM details | 200 with correct id, name, status | PASS |
| Create duplicate VM | 409 Conflict | `409 {"detail":"VM 'e2e-final' already exists","status":409,"title":"Conflict"}` | PASS |
| Get non-existent VM | 404 Not Found | `404 {"detail":"VM '...' not found","status":404,"title":"Not Found"}` | PASS |
| Create VM with missing fields | 400 Bad Request | `400` with OpenAPI validation message | PASS |
| Delete VM | 204 No Content | **500** (kweb DELETE bug - returns HTML) | KNOWN BUG |

### Cluster Lifecycle

| Test | Expected | Actual | Status |
|---|---|---|---|
| Create k3s cluster | 201, status=CREATING | `201 {"id":"...","name":"e2e-k3s","status":"CREATING"}` | PASS |
| Cluster transitions to ACTIVE | Status changes after kweb creates cluster | Status=ACTIVE | PASS |
| List clusters | Returns managed clusters | 200 with `results` array | PASS |
| Get cluster (live kweb data) | Returns cluster with version | 200, version="N/A" (kweb returns N/A for new clusters) | PASS |
| Create cluster with invalid type "kind" | 400 Bad Request | `400` with OpenAPI enum validation | PASS |
| Get non-existent cluster | 404 Not Found | `404` with RFC 7807 body | PASS |

### SPM Generic Resource Protocol (Full DCM Flow)

| Test | Expected | Actual | Status |
|---|---|---|---|
| Create VM via catalog-item-instances | 201, VM provisioned via kcli SP | `201` catalog instance created, SP received `POST /vms?id=<uuid>`, VM created in kweb | **PASS** |
| SPM forwards spec to SP | `POST {endpoint}?id=<uuid>` with `{"spec": ...}` | SP log: `POST /vms?id=694347de-... - 201 248B` | **PASS** |
| VM visible in service-type-instances | Instance with status RUNNING | `{"id":"694347de-...","status":"RUNNING","provider_name":"kcli-vm"}` | **PASS** |
| VM visible via kcli | VM running in libvirt | `dcm-694347de-...` status=up, profile=fedora41 | **PASS** |
| Catalog spec resolution | user_values merged into spec | Spec contains `guest_os.type`, `memory.size`, `vcpu.count` | **PASS** |
| SP handles missing metadata.name | Derive name from SPM instance ID | kcli name = `dcm-694347de-0bc4-438b-834d-91402d46c98f` | **PASS** |

### NATS CloudEvents

| Test | Expected | Actual | Status |
|---|---|---|---|
| VM status change publishes event | CloudEvent on `dcm.vm` subject | Received: `{"specversion":"1.0","source":"dcm/providers/kcli-vm","type":"dcm.status.vm","data":{"id":"...","status":"RUNNING",...}}` | PASS |
| Event format is valid CloudEvent | specversion, id, source, type, data fields | All fields present, compliant with CloudEvents 1.0 | PASS |

### Error Handling

| Test | Expected | Actual | Status |
|---|---|---|---|
| RFC 7807 problem details on 404 | `application/problem+json` format | Correct: status, title, detail, type fields | PASS |
| RFC 7807 problem details on 409 | Conflict body | Correct format | PASS |
| RFC 7807 problem details on 500 | Server error body | Correct format with kweb error detail | PASS |
| OpenAPI validation on 400 | Validation message | Clear message from nethttp-middleware (plain text, not JSON) | PASS |

## Bugs Found and Fixed

### BUG-001: Duplicate VM creation returned 500 instead of 409

**Root cause:** kweb returns HTTP 200 with `{"result": "failure", "reason": "VM ... already exists"}` instead of HTTP 409. The kweb client's `parseResponse` method detected the failure but returned a generic `KwebError` instead of `ErrConflict`, because the "already exists" detection only existed in `parseErrorBody` (called for HTTP 4xx+).

**Fix:** Updated `parseResponse` to check for "already exists" in the failure reason within the HTTP 200 success path and return `ErrConflict` directly. Also added a test for generic non-conflict failures (e.g., "quota exceeded") to ensure those still return `KwebError`.

**Files changed:**
- `internal/kweb/client.go` - Added conflict detection in `parseResponse` for 200-range responses
- `internal/kweb/client_unit_test.go` - Updated test and added coverage for non-conflict failures

### BUG-002: SPM catalog flow returned 400 due to required metadata field

**Root cause:** The VMSpec and ClusterSpec schemas in `openapi.yaml` required `metadata` (with `name`) and `guest_os`, but the catalog-manager's resolved spec does not include a `metadata` object. When SPM forwarded the resolved spec to the SP, the OpenAPI request validator middleware rejected the request with `property "metadata" is missing`.

**Fix:** Made `metadata` and `guest_os` optional in VMSpec and ClusterSpec. Added `resolveVMName()` and `resolveClusterName()` helpers that fall back to the SPM-provided instance ID when `metadata.name` is absent. Updated `resolveVMProfile()` to handle nil `GuestOs` with a sensible default.

**Files changed:**
- `api/v1alpha1/openapi.yaml` - Removed `metadata` and `guest_os` from required fields
- `internal/api/server/impl.go` - Added name resolution helpers, nil-safe field access
- Regenerated: `api/v1alpha1/types.gen.go`, `api/v1alpha1/spec.gen.go`, `internal/api/server/server.gen.go`, `pkg/client/client.gen.go`

## Known Issues (Not Bugs in Our Code)

### kweb DELETE returns HTML 500

kweb's `DELETE /vms/{name}` endpoint returns an HTML error page instead of JSON. This is tracked in [karmab/kcli#863](https://github.com/karmab/kcli/issues/863). Our SP correctly detects the HTML response and returns an RFC 7807 error. Workaround: delete VMs via `kcli delete vm <name> -y`.

### kweb default port changed in kcli v99.0

kweb now defaults to port **8000** (was 18000 in earlier versions). The `kcli web` subcommand was renamed to a standalone `kweb` binary.

### No VM profiles configured

kweb returned an empty profiles map (`{"profiles": {}}`). This means the profile validation cache is empty, but CreateVM still works because kweb itself validates the profile name.

## Lessons Learned

1. **kweb API quirks are real.** The HTTP 200 + `result:failure` pattern for conflicts is non-standard and required a targeted fix. Testing against real kweb is essential.
2. **Dual registration works correctly** against the real SPM, confirming the implementation.
3. **NATS CloudEvents flow end-to-end**, from the monitor detecting status changes to publishing on the `dcm.vm` subject.
4. **OpenAPI validation middleware works** in production, catching malformed requests before they reach handlers.
5. **Cross-compilation** (amd64 → arm64) with static linking produced a working binary with zero runtime dependencies.
6. **Catalog-manager spec resolution** does not include `metadata` or other fields not defined in the catalog item's `fields` array. Service providers must handle optional metadata gracefully by deriving names from the SPM instance ID.
7. **Collection-level endpoint registration** (`/vms`, `/clusters`) is critical for SPM to route creation requests correctly. The previous base-level registration (`/api/v1alpha1`) caused 404 errors.
8. **Stable provider IDs** (`PROVIDER_ID_VM`, `PROVIDER_ID_CLUSTER`) prevent 409 conflicts on SP restart. Without them, each restart generates new UUIDs and conflicts with stale registrations.
9. **Container-reachable LISTEN_ADDRESS** (e.g., `10.89.0.1:8085` for podman bridge gateway) is required when the SP runs on the host and SPM runs in a container. Using `:8085` alone causes SPM health checks to fail.

See also: [`LESSONS_LEARNED.md`](LESSONS_LEARNED.md) for a broader guide on
building DCM service providers.

---

## E2E Regression Test (Post-Lint-Fix)

**Date:** 2026-04-22 (same day, after comprehensive linting cleanup)
**Purpose:** Verify the kcli SP still works correctly after 186 lint fixes across
15 files, including changes to production code (error handling, control flow
refactoring, unused code removal).

### Context

A comprehensive `golangci-lint` cleanup was performed to bring the codebase into
compliance with the DCM project's standard linter configuration (30+ linters).
Changes included:

- Rewriting `if-else` chains as `switch` statements (`gocritic`)
- Removing an unused `healthResponse` method
- Renaming unused parameters to `_` in production code (`revive`)
- Adding `//nolint` directives for intentional error suppression patterns
- Formatting changes from `gofumpt`

These are not cosmetic-only changes — some touched control flow in handlers and
the monitor. A full regression test was required.

### Regression Test Results

| Phase | Test | Result |
|-------|------|--------|
| **Build & Deploy** | Cross-compile arm64 binary, SCP to Apollo, start with same provider IDs | **PASS** — both providers registered and `ready` in SPM |
| **Smoke: /health** | `GET /health` | **PASS** — `{"status":"pass"}` |
| **Smoke: /vms/health** | `GET /vms/health` | **PASS** — `{"status":"pass"}` |
| **Smoke: /clusters/health** | `GET /clusters/health` | **PASS** — `{"status":"pass"}` |
| **Smoke: List VMs** | `GET /vms` | **PASS** — returns existing VMs |
| **Smoke: List Clusters** | `GET /clusters` | **PASS** — returns empty list |
| **VM Lifecycle: Create via catalog** | `POST /catalog-item-instances` with Fedora VM catalog item | **PASS** — 201, SPM delegated `POST /vms?id=<uuid>` to SP |
| **VM Lifecycle: Verify in kweb** | `curl http://localhost:8000/vms` | **PASS** — `dcm-<uuid>` visible with status `up` |
| **VM Lifecycle: Monitor detects RUNNING** | Wait for poll cycle | **PASS** — status transitions from PROVISIONING to RUNNING |
| **VM Lifecycle: Delete** | `DELETE /vms/<id>` | **KNOWN ISSUE** — kweb DELETE returns 500 ([karmab/kcli#863](https://github.com/karmab/kcli/issues/863)); SP correctly propagates as RFC 7807 |
| **VM Lifecycle: Cleanup via kcli CLI** | `kcli delete vm dcm-<uuid>` | **PASS** — VM deleted, monitor removes from store |
| **Cluster Lifecycle: Create via SP** | `POST /clusters?id=test-regression-cluster` | **PASS** — 201, cluster created in kweb |
| **Cluster Lifecycle: Delete** | `DELETE /clusters/test-regression-cluster` | **PASS** — 204 No Content, removed from SP and kweb |
| **Idempotent Create** | POST same VM ID twice | **PASS** — second call returns 201 with existing resource |
| **Conflict Handling** | Create VM that already exists in kweb | **PASS** — 409 with RFC 7807 problem detail |
| **Health Degradation** | Stop kweb, check health endpoints | **PASS** — all return `{"status":"fail","message":"kweb is unreachable"}` |
| **Health Recovery** | Restart kweb, check health endpoints | **PASS** — all return `{"status":"pass"}` |

### Verdict

**All tests pass.** The lint fixes did not introduce any regressions. The only
failure is the pre-existing upstream kweb DELETE bug
([karmab/kcli#863](https://github.com/karmab/kcli/issues/863)), which is not
related to our code changes.
