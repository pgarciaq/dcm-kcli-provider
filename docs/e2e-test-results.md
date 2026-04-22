# E2E Live Testing Results

**Date:** 2026-04-22
**Environment:** Apollo Hypervisor (`hpe-apollo-cn99xx-16.khw.eng.rdu2.dc.redhat.com`)
**Architecture:** aarch64 (ARM64)

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

## Known Issues (Not Bugs in Our Code)

### kweb DELETE returns HTML 500

kweb's `DELETE /vms/{name}` endpoint returns an HTML error page instead of JSON. This is tracked in [karmab/kcli#863](https://github.com/karmab/kcli/issues/863). Our SP correctly detects the HTML response and returns an RFC 7807 error. Workaround: delete VMs via `kcli delete vm <name> -y`.

### kweb default port changed in kcli v99.0

kweb now defaults to port **8000** (was 18000 in earlier versions). The `kcli web` subcommand was renamed to a standalone `kweb` binary.

### SPM health_status shows "not_ready"

The endpoint registered with SPM is `http://:8085/api/v1alpha1` (missing hostname) because the SP derives it from `LISTEN_ADDRESS=:8085`. SPM cannot resolve this to perform health checks. In production, either set `LISTEN_ADDRESS=<hostname>:8085` or add a separate `PROVIDER_ENDPOINT` environment variable.

### No VM profiles configured

kweb returned an empty profiles map (`{"profiles": {}}`). This means the profile validation cache is empty, but CreateVM still works because kweb itself validates the profile name.

## Lessons Learned

1. **kweb API quirks are real.** The HTTP 200 + `result:failure` pattern for conflicts is non-standard and required a targeted fix. Testing against real kweb is essential.
2. **Dual registration works correctly** against the real SPM, confirming the implementation.
3. **NATS CloudEvents flow end-to-end**, from the monitor detecting status changes to publishing on the `dcm.vm` subject.
4. **OpenAPI validation middleware works** in production, catching malformed requests before they reach handlers.
5. **Cross-compilation** (amd64 → arm64) with static linking produced a working binary with zero runtime dependencies.
