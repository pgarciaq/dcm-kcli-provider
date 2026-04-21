---
title: kcli-sp
authors:
  - "@pgarciaq"
reviewers:
  - TBD
approvers:
  - TBD
creation-date: 2026-04-17
see-also:
  - "/enhancements/kubevirt-sp/kubevirt-sp.md"
  - "/enhancements/acm-cluster-sp/acm-cluster-sp.md"
  - "/enhancements/sp-registration-flow/sp-registration-flow.md"
  - "/enhancements/service-provider-health-check/service-provider-health-check.md"
  - "/enhancements/state-management/service-provider-status-reporting.md"
  - "/enhancements/service-type-definitions/service-type-definitions.md"
---

# kcli Service Provider

## Open Questions

1. **kweb version pinning.** kweb has no versioned API contract. Should the
   SP pin to a specific kcli release and test against it, or attempt to
   support multiple kweb versions with feature detection?

2. **Multi-backend status mapping.** kweb VM status strings vary by backend
   (libvirt returns `up`/`down`; vSphere adds `suspended`; OpenStack has
   `error`). Should the SP implement per-backend mapping tables, or
   document libvirt as the only supported backend for v1?

3. **Cluster type `kind`.** kweb's `swagger.yml` lists `kind` as a valid
   cluster type, but the implementation has no handler for it (causing a
   runtime error). Should the SP reject `kind` with a clear error, or
   attempt to support it once kweb is fixed upstream?

## Summary

The kcli Service Provider is a DCM Service Provider that manages virtual
machines and Kubernetes clusters through
[kcli](https://github.com/karmab/kcli)'s HTTP API (kweb). It is designed for
**development, testing, and homelab environments** — not for production
workloads. Unlike the existing KubeVirt SP and ACM Cluster SP — which interact
directly with Kubernetes CRDs on a management cluster — the kcli SP
communicates with a standalone kweb instance, enabling DCM to provision
infrastructure on any hypervisor backend that kcli supports
(primarily libvirt/KVM for homelab use).

Because DCM registration is per service type, the kcli SP registers **twice**
with the Service Provider Manager: once for the `vm` service type and once for
the `cluster` service type. From DCM's perspective these appear as two
independent providers (`kcli-vm` and `kcli-cluster`), but they share a single
Go binary and a single kweb backend.

## Motivation

The existing DCM service providers cover two specific platforms:

- **KubeVirt SP** — manages VMs on a Kubernetes cluster running KubeVirt.
- **ACM Cluster SP** — manages OpenShift clusters via Advanced Cluster
  Management and HyperShift.

Both require a Kubernetes cluster as their management plane. This leaves a gap
for **developers and homelab operators** who want to use DCM without deploying
a full Kubernetes management stack:

1. **Developer inner loop:** Engineers building DCM integrations, service
   providers, or UIs need a lightweight backend to provision real VMs and
   clusters without access to a KubeVirt or ACM environment.
2. **Homelab experimentation:** Enthusiasts running libvirt/KVM on a personal
   server want to manage their VMs and clusters through DCM's unified API.
3. **CI/CD test environments:** Automated test pipelines need a disposable
   provider that can spin up VMs and clusters on bare-metal or nested-virt CI
   runners.

kcli fills this gap. It is a mature, open-source tool that wraps libvirt and
other hypervisors behind a unified API. Its HTTP interface (kweb) exposes both
VM and Kubernetes cluster lifecycle operations, making it a natural fit as a
lightweight DCM backend for non-production use.

### Goals

- Provide a **lightweight, easy-to-deploy** DCM provider for development and
  homelab use.
- Define the lifecycle of a DCM SP that manages VMs and Kubernetes clusters
  through kweb.
- Define the dual registration flow (one per service type) with the DCM SP
  API.
- Define CREATE, READ, and DELETE endpoints for both VMs and clusters.
- Define status reporting for DCM requests.
- Define the kweb HTTP client contract and error normalization strategy.
- Minimize operational prerequisites: no Kubernetes management cluster, no
  mTLS infrastructure, no external databases.

### Non-Goals

- **Production workloads.** This SP is for development, testing, and homelab
  environments. Production deployments should use the KubeVirt SP or ACM
  Cluster SP.
- Day 2 operations (stop, start, restart, scale, snapshot, migrate) for VMs
  or clusters.
- Adding authentication to kweb itself (the homelab deployment model assumes
  a trusted network).
- Implementing a kweb instance manager or deployer; kweb is assumed to be
  pre-deployed.
- Supporting kcli plans, products, containers, networks, pools, or repos
  through DCM.
- Supporting kcli's CLI directly (this SP uses the HTTP API exclusively).
- Defining the UPDATE endpoint (out of scope for v1).
- High availability or multi-replica deployment (not needed for dev/homelab).

## Proposal

### User Stories

#### Story 1: DCM Developer

As a developer working on DCM integrations, I want a provider I can run on
my laptop (libvirt/KVM) so that I can test VM and cluster provisioning
through the DCM API without needing access to a KubeVirt or ACM environment.

#### Story 2: Homelab Operator

As a homelab enthusiast running libvirt on a personal server, I want to
manage my VMs and k3s clusters through DCM's unified API instead of
switching between `virsh` and `kcli` commands.

#### Story 3: CI Pipeline

As a CI/CD pipeline, I need a lightweight DCM provider that can provision
disposable VMs and clusters on bare-metal or nested-virt runners to test
DCM workflows end-to-end without cloud infrastructure costs.

### Assumptions

- A kweb instance is deployed, running, and reachable over the network from
  the kcli SP binary.
- The kweb instance has valid credentials for its configured backend (e.g.,
  libvirt socket access).
- The deployment is on a **trusted network** (homelab LAN, developer
  workstation, or CI runner). kweb has **no built-in authentication** and
  exposes destructive operations and sensitive data (cluster-admin
  kubeconfigs) to any network client that can reach it. For
  non-trusted networks, deploying kweb behind a reverse proxy with
  mTLS or token-based authentication is **strongly recommended** — see
  Risks and Mitigations.
- Bidirectional network connectivity: the SP must reach kweb (HTTP), DCM
  must reach the SP (HTTP for health checks and provisioning requests), and
  the SP must reach NATS (for status events).
- The DCM Service Provider Registry is reachable for registration.
- The DCM messaging system (NATS) is reachable for status reporting.
- Each kweb instance manages a single backend environment. Multi-environment
  setups require one kweb + one kcli SP instance per environment.

### Integration Points

#### kweb Integration

The kcli SP communicates with kweb over HTTP using a hand-written thin
client. The SP does **not** rely on kweb's `swagger.yml` for client
generation due to known spec drift (see Risks). The relevant kweb endpoints
are:

**VM operations:**

| Method | kweb Endpoint | Purpose |
|--------|---------------|---------|
| POST | `/vms` | Create a VM (synchronous, returns HTTP 200) |
| GET | `/vms` | List all VMs |
| GET | `/vms/{name}` | Get VM details |
| DELETE | `/vms/{name}` | Delete a VM (returns HTTP 200) |
| POST | `/vms/{name}/start` | Start a VM |
| POST | `/vms/{name}/stop` | Stop a VM |

**Cluster operations:**

| Method | kweb Endpoint | Purpose |
|--------|---------------|---------|
| POST | `/kubes` | Create a cluster (async — returns immediately) |
| GET | `/kubes` | List all clusters |
| GET | `/kubes/{name}` | Get cluster status |
| DELETE | `/kubes/{name}` | Delete a cluster |
| GET | `/kubes/{name}/kubeconfig` | Retrieve kubeconfig (plain text) |

**Health probing:**

| Method | kweb Endpoint | Purpose |
|--------|---------------|---------|
| GET | `/host` | Backend connectivity check |

kweb returns JSON responses for most endpoints. The notable exception is
`GET /kubes/{name}/kubeconfig`, which returns **raw kubeconfig text**
(`text/plain`), not JSON.

VM creation is synchronous (the handler blocks until the VM is created or
fails). Cluster creation is asynchronous (kweb spawns a background thread
and returns `{"result": "success"}` immediately; the SP must poll
`GET /kubes/{name}` to track provisioning progress).

kweb success responses use **HTTP 200** (not 201). The SP translates these
to the appropriate DCM HTTP status codes (201 for creates, 204 for deletes).

kweb's error responses are **inconsistent**: some endpoints return JSON
`{"result": "failure", "reason": "..."}`, others return plain strings
(`"Invalid data"`) or bare HTTP status codes with an empty body (`{}`). The
SP's kweb client layer must normalize all error responses into a consistent
internal representation.

#### DCM SP Registry

Auto-registration on startup with DCM SP Registrar. The kcli SP registers
**twice** — once for each service type. See documentation for
[DCM Registration Flow](https://github.com/dcm-project/enhancements/blob/main/enhancements/sp-registration-flow/sp-registration-flow.md)
and
[registration client library](https://github.com/dcm-project/service-provider-api/tree/main/pkg/registration/client).

#### DCM SP Health Check

The kcli SP must expose a health endpoint at `GET /health` (root path) for
the DCM control plane to poll every 10 seconds. The health check verifies:

1. The SP process is running (implicit from responding to HTTP).
2. kweb is reachable (the SP calls `GET /host` on kweb and checks for a
   valid JSON response). Note: kweb's `/host` handler has no try/except; if
   the backend connection is broken, it may return HTTP 500 instead of a
   structured error. The SP treats any non-200 kweb response as unhealthy.

**Expected response (HTTP 200 OK):**

```json
{
  "status": "pass",
  "version": "0.1.0",
  "uptime": 3600
}
```

The response body follows the format defined in the
[SP Health Check](https://github.com/dcm-project/enhancements/blob/main/enhancements/service-provider-health-check/service-provider-health-check.md)
enhancement. The `status` field uses `"pass"` (not `"healthy"`) per the
contract.

#### DCM SP Status Reporting

Publish status updates for VM and cluster instances to the messaging system
using CloudEvents format. Events are published to subjects based on the
service type, per the
[SP Status Reporting](https://github.com/dcm-project/enhancements/blob/main/enhancements/state-management/service-provider-status-reporting.md)
enhancement:

- VMs: `dcm.vm`
- Clusters: `dcm.cluster`

Provider identity and instance identifiers are carried in the CloudEvent
envelope attributes (`source`, `subject`), not in the NATS subject. This
keeps routing simple and aligns with the canonical contract.

### Registration Flow

The kcli SP must successfully register with DCM for each service type it
provides. During startup, after the HTTP server is ready, the SP uses the DCM
registration client to send two requests to the SP API registration endpoint:
`POST /api/v1alpha1/providers`.

See DCM
[registration flow](https://github.com/dcm-project/enhancements/blob/main/enhancements/sp-registration-flow/sp-registration-flow.md)
for more information.

#### VM Registration

```golang
dcm "github.com/dcm-project/service-provider-api/pkg/registration/client"
...
request := &dcm.RegistrationRequest{
    Name:        "kcli-vm",
    ServiceType: "vm",
    DisplayName: "kcli VM Service Provider",
    Endpoint:    fmt.Sprintf("%s/api/v1alpha1/vms", apiHost),
    Metadata: dcm.Metadata{
        Region: config.Region,
        Zone:   config.Zone,
    },
    Operations: []string{"CREATE", "DELETE", "READ"},
}
```

#### Cluster Registration

```golang
dcm "github.com/dcm-project/service-provider-api/pkg/registration/client"
...
request := &dcm.RegistrationRequest{
    Name:        "kcli-cluster",
    ServiceType: "cluster",
    DisplayName: "kcli Cluster Service Provider",
    Endpoint:    fmt.Sprintf("%s/api/v1alpha1/clusters", apiHost),
    Metadata: dcm.Metadata{
        Region: config.Region,
        Zone:   config.Zone,
    },
    Operations: []string{"CREATE", "DELETE", "READ"},
}
```

#### Registration Request Validation

The registration payloads must conform to the validation requirements defined
in the
[SP registration flow](https://github.com/dcm-project/enhancements/blob/main/enhancements/sp-registration-flow/sp-registration-flow.md).

**kcli SP-specific requirements:**

- The VM registration `serviceType` field must be `"vm"`.
- The cluster registration `serviceType` field must be `"cluster"`.
- `operations` must include at minimum: `CREATE`, `READ`, `DELETE`.
- `metadata` fields (`region`, `zone`) are populated from SP configuration.
  Resource capacity metadata is **not** provided at registration time because
  kweb does not expose a resource inventory API.

#### Registration Process

1. The SP binary starts and initializes the HTTP listener.
2. After the server is ready (via `WithOnReady` callback), registration runs
   in background goroutines — one for VMs, one for clusters.
3. Each registration request is sent to the DCM Service Provider Registry.
4. On success, the SP is registered and available for DCM to route requests.
5. Registration failures are retried with exponential backoff. Failures do
   not block server startup. Alternatively, the SP can fall back to manual
   registration by an administrator.
6. Both registrations share the same kweb backend; the `Endpoint` field
   differs to route VM requests and cluster requests to different handler
   paths within the SP.

### API Endpoints

The kcli SP exposes two groups of CRUD endpoints, one per service type. Both
groups are served by the same HTTP server on the same port.

#### VM Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | /api/v1alpha1/vms | Create a new VM |
| GET | /api/v1alpha1/vms | List all VMs |
| GET | /api/v1alpha1/vms/{vmId} | Get a VM instance |
| DELETE | /api/v1alpha1/vms/{vmId} | Delete a VM instance |

#### Cluster Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | /api/v1alpha1/clusters | Create a new cluster |
| GET | /api/v1alpha1/clusters | List all clusters |
| GET | /api/v1alpha1/clusters/{clusterId} | Get a cluster instance |
| DELETE | /api/v1alpha1/clusters/{clusterId} | Delete a cluster |

#### Common Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | /health | SP health check (root path per DCM contract) |

##### AEP Compliance

All endpoints under `/api/v1alpha1/` are defined based on AEP standards and
use `aep-openapi-linter` to check for compliance.

#### POST /api/v1alpha1/vms — Create a VM

The POST endpoint follows the contract defined in the VM schema spec
pre-defined by DCM core. See
[VM Schema](https://github.com/dcm-project/enhancements/blob/main/enhancements/service-type-definitions/service-type-definitions.md).
The SP translates the DCM VM request into a kweb `POST /vms` call.

The SP generates a `dcm-instance-id` UUID for tracking and includes it in
its internal state. Since kweb does not support arbitrary labels or metadata
on VMs, the SP maintains an internal mapping between `dcm-instance-id` and
kcli VM names. This differs from the Kubernetes-based SPs (KubeVirt, K8s
Container) which label managed resources with `managed-by=dcm` and
`dcm-instance-id` directly on the CRs.

Example request payload:

```json
{
  "memory": { "size": "4GB" },
  "vcpu": { "count": 2 },
  "guestOS": { "type": "fedora-39" },
  "access": {
    "sshPublicKey": "ssh-ed25519 AAAAC3..."
  },
  "metadata": { "name": "web-server" },
  "serviceType": "vm"
}
```

Example response payload (201 Created):

```json
{
  "id": "123e4567-e89b-12d3-a456-426614174000",
  "name": "web-server",
  "status": "PROVISIONING"
}
```

The SP translates this into a kweb-compatible request. kweb's `POST /vms`
handler requires both a `name` and a `profile` field. Additional parameters
(memory, CPUs, disks, nets) are passed as overrides:

```json
{
  "name": "web-server",
  "profile": "fedora-39",
  "parameters[memory]": 4096,
  "parameters[numcpus]": 2,
  "parameters[keys]": ["ssh-ed25519 AAAAC3..."]
}
```

The `profile` field maps to a kcli VM profile that defines the base image,
disk layout, and default parameters. The SP maps the DCM `guestOS.type`
field to the appropriate kcli profile name.

**Error Handling:**

- **400 Bad Request**: Invalid request payload or missing required fields.
- **409 Conflict**: VM with the same `metadata.name` already exists in kweb.
- **500 Internal Server Error**: Unexpected error from kweb during
  creation.
- **502 Bad Gateway**: kweb is unreachable or returned a non-JSON error.

#### POST /api/v1alpha1/clusters — Create a Cluster

The SP translates the DCM cluster request into a kweb `POST /kubes` call.
Cluster creation is asynchronous on the kweb side; the SP returns `CREATING`
immediately and polls kweb for completion.

Example request payload:

```json
{
  "clusterType": "k3s",
  "controlPlane": { "count": 1 },
  "workers": { "count": 2 },
  "metadata": { "name": "edge-cluster" },
  "serviceType": "cluster"
}
```

Example response payload (201 Created):

```json
{
  "id": "456e7890-e89b-12d3-a456-426614174001",
  "name": "edge-cluster",
  "status": "CREATING"
}
```

Supported cluster types (mapped from kweb):

| DCM clusterType | kweb kubetype | Notes |
|-----------------|---------------|-------|
| `generic` | `generic` | Kubeadm-based vanilla Kubernetes |
| `k3s` | `k3s` | Lightweight Kubernetes |
| `openshift` | `openshift` | OpenShift (requires pull secret) |
| `microshift` | `microshift` | Single-node edge OpenShift |
| `hypershift` | `hypershift` | HyperShift hosted control plane |

Note: kweb's `swagger.yml` also lists `kind` as a cluster type, but the
implementation has no handler for it — sending `kind` causes an
`UnboundLocalError` in kweb. The SP must **reject** `kind` requests with a
`400 Bad Request` and a descriptive error message until kweb adds support.

**Error Handling:**

- **400 Bad Request**: Invalid payload, missing fields, or unsupported
  cluster type (including `kind`).
- **500 Internal Server Error**: Unexpected error from kweb.
- **502 Bad Gateway**: kweb is unreachable.

#### GET /api/v1alpha1/vms — List VMs

Returns all VMs managed by this SP, with pagination support per AEP-132.

**Query Parameters:**

- `max_page_size` (optional): Maximum number of resources per page.
  Default: 50.
- `page_token` (optional): Token for the next page.

**Process Flow:**

1. Handler receives GET request with optional pagination parameters.
2. Calls `GET /vms` on kweb (which returns all VMs).
3. Filters to VMs tracked in the internal state store.
4. Applies pagination and returns the current page.

**Example Response Payload:**

```json
{
  "results": [
    {
      "id": "123e4567-e89b-12d3-a456-426614174000",
      "name": "web-server",
      "status": "RUNNING",
      "ip": "192.168.122.45"
    },
    {
      "id": "789a0123-e89b-12d3-a456-426614174002",
      "name": "db-server",
      "status": "STOPPED"
    }
  ],
  "next_page_token": "a1b2c3d4"
}
```

Note: kweb's `GET /vms` returns **all** VMs with enriched info (IPs, MACs,
disk paths, network details, users). The SP filters this to DCM-managed VMs
and redacts sensitive fields before returning.

**Error Handling:**

- **400 Bad Request**: Invalid pagination parameters.
- **502 Bad Gateway**: kweb is unreachable.

#### GET /api/v1alpha1/vms/{vmId}

Returns detailed VM information. The SP resolves the `dcm-instance-id` to a
kcli VM name, calls `GET /vms/{name}` on kweb, maps the response to the DCM
VM schema, and enriches it with the `dcm-instance-id`.

Example response payload:

```json
{
  "id": "123e4567-e89b-12d3-a456-426614174000",
  "name": "web-server",
  "status": "RUNNING",
  "ip": "192.168.122.45",
  "ssh": {
    "enabled": true,
    "username": "fedora"
  }
}
```

**Error Handling:**

- **404 Not Found**: No VM with the specified `vmId` in the state store.
- **502 Bad Gateway**: kweb is unreachable or returned an error for the VM.

#### GET /api/v1alpha1/clusters — List Clusters

Returns all clusters managed by this SP, with pagination per AEP-132.
Follows the same pagination contract as the VM list endpoint.

**Error Handling:**

- **400 Bad Request**: Invalid pagination parameters.
- **502 Bad Gateway**: kweb is unreachable.

#### GET /api/v1alpha1/clusters/{clusterId}

Returns cluster status. The SP calls `GET /kubes/{name}` on kweb and maps
the response to the DCM cluster schema. If the cluster is ready, the
kubeconfig is also available via `GET /kubes/{name}/kubeconfig` on kweb.

Example response payload:

```json
{
  "id": "456e7890-e89b-12d3-a456-426614174001",
  "name": "edge-cluster",
  "status": "ACTIVE",
  "nodes": "3",
  "version": "v1.30.2+k3s1"
}
```

**Error Handling:**

- **404 Not Found**: No cluster with the specified `clusterId`.
- **502 Bad Gateway**: kweb is unreachable.

#### DELETE /api/v1alpha1/vms/{vmId}

Deletes a VM. The SP resolves the `dcm-instance-id` to a kcli VM name and
calls `DELETE /vms/{name}` on kweb. Returns `204 No Content`. The SP
publishes a `DELETED` status event and removes the entry from the internal
state store.

**Error Handling:**

- **404 Not Found**: No VM with the specified `vmId`.
- **502 Bad Gateway**: kweb is unreachable.

#### DELETE /api/v1alpha1/clusters/{clusterId}

Deletes a cluster and all its associated VMs. The SP resolves the
`dcm-instance-id` to a kcli cluster name and calls `DELETE /kubes/{name}` on
kweb. Returns `204 No Content`.

**Error Handling:**

- **404 Not Found**: No cluster with the specified `clusterId`.
- **502 Bad Gateway**: kweb is unreachable.

#### GET /health

Returns health status. Exposed at the **root path** (not under
`/api/v1alpha1/`) per the DCM
[health check contract](https://github.com/dcm-project/enhancements/blob/main/enhancements/service-provider-health-check/service-provider-health-check.md).

The SP probes kweb's `GET /host` endpoint to verify backend connectivity.

**Healthy response (HTTP 200 OK):**

```json
{
  "status": "pass",
  "version": "0.1.0",
  "uptime": 3600
}
```

**Unhealthy response (HTTP 503 Service Unavailable):**

```json
{
  "status": "fail",
  "version": "0.1.0",
  "uptime": 3600,
  "message": "kweb unreachable at http://kweb:9000"
}
```

### SP Configuration

The kcli SP is configured via environment variables, consistent with the
other DCM providers.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LISTEN_ADDRESS` | No | `:8080` | SP HTTP server bind address |
| `KWEB_URL` | Yes | — | kweb base URL (e.g., `http://kweb:9000`) |
| `SPM_URL` | Yes | — | Service Provider Manager URL |
| `NATS_URL` | No | — | NATS server URL for status events |
| `PROVIDER_NAME_VM` | No | `kcli-vm` | Registration name for VM service |
| `PROVIDER_NAME_CLUSTER` | No | `kcli-cluster` | Registration name for cluster service |
| `REGION` | No | — | Region metadata for registration |
| `ZONE` | No | — | Zone metadata for registration |
| `POLL_INTERVAL` | No | `30s` | Interval for polling kweb for status |
| `DEBOUNCE_WINDOW` | No | `5s` | Minimum interval between status updates for the same resource |
| `STATE_STORE_PATH` | No | `/data/state.db` | Path to the persistent bbolt state store |
| `LOG_LEVEL` | No | `info` | Log verbosity (debug, info, warn, error) |

### Status Reporting to DCM

Since kweb does not provide a watch/informer mechanism, the kcli SP uses a
**polling-based status monitor** instead of the Kubernetes informer pattern
used by other DCM providers.

#### Polling Architecture

The SP runs a background goroutine that periodically:

1. Calls `GET /vms` and `GET /kubes` on kweb.
2. Compares the current state with the last known state in the store.
3. For any resource whose status has changed, applies debounce logic (see
   below) and publishes a CloudEvents status update to NATS.

The poll interval is configurable (default 30 seconds). This is a trade-off:
shorter intervals increase load on kweb but reduce status reporting latency.
At homelab scale (tens of resources), the default interval imposes negligible
load on kweb.

#### Debounce Logic

To avoid flooding the messaging system during rapid status oscillation (as
recommended by the
[SP Status Reporting](https://github.com/dcm-project/enhancements/blob/main/enhancements/state-management/service-provider-status-reporting.md)
enhancement), the SP enforces a configurable debounce window
(`DEBOUNCE_WINDOW`, default 5 seconds). If a resource's status changes
multiple times within the window, only the final state is published.

#### Internal State Store

The SP maintains a persistent mapping ([bbolt](https://github.com/etcd-io/bbolt) —
the etcd-maintained fork of BoltDB) of:

- `dcm-instance-id` → kcli resource name (VM or cluster)
- `dcm-instance-id` → last known status
- `dcm-instance-id` → resource type (`vm` or `cluster`)
- `dcm-instance-id` → creation timestamp

This store is required because kweb has no concept of DCM instance IDs. The
SP is the translation layer between DCM's UUID-based resource model and
kcli's name-based model.

**Durability:** The store is persisted to disk at `STATE_STORE_PATH`. In
containerized deployments, this path should be backed by a persistent volume
to survive container restarts. In a homelab, a host-mounted directory is
sufficient.

**Recovery on restart:** The SP reloads the store from disk and reconciles
against kweb by listing all resources. Resources in the store that no longer
exist in kweb are marked `DELETED`. Resources in kweb that are not in the
store are logged as orphans (created outside DCM or from a lost store).

**If the store is lost:** At homelab scale, the simplest recovery is to
delete the tracked resources from DCM and re-create them (or manually
re-associate them). This is an acceptable trade-off for the simplicity of
a single-file embedded store.

#### CloudEvents Format

Status updates are published to NATS using the CloudEvents specification
(v1.0). The NATS subject is determined by the service type per the canonical
contract: `dcm.vm` for VMs, `dcm.cluster` for clusters.

**VM events:**

```golang
cloudevents "github.com/cloudevents/sdk-go/v2"

type VMStatus struct {
    Id      string `json:"id"`
    Status  string `json:"status"`
    Message string `json:"message"`
}

event := cloudevents.NewEvent()
event.SetID("event-uuid")
event.SetSource("dcm/providers/kcli-vm")
event.SetType("dcm.status.vm")
event.SetSubject("dcm.vm")
event.SetData(cloudevents.ApplicationJSON, VMStatus{
    Id:      "123e4567-e89b-12d3-a456-426614174000",
    Status:  "RUNNING",
    Message: "VM is running at 192.168.122.45",
})
```

**Cluster events:**

```golang
type ClusterStatus struct {
    Id      string `json:"id"`
    Status  string `json:"status"`
    Message string `json:"message"`
}

event := cloudevents.NewEvent()
event.SetID("event-uuid")
event.SetSource("dcm/providers/kcli-cluster")
event.SetType("dcm.status.cluster")
event.SetSubject("dcm.cluster")
event.SetData(cloudevents.ApplicationJSON, ClusterStatus{
    Id:      "456e7890-e89b-12d3-a456-426614174001",
    Status:  "ACTIVE",
    Message: "Cluster is ready with 3 nodes",
})
```

#### VM Status Mapping

Providers must normalize backend-specific states to the DCM generic status
enum defined in the
[SP Status Reporting](https://github.com/dcm-project/enhancements/blob/main/enhancements/state-management/service-provider-status-reporting.md)
enhancement. The canonical VM lifecycle phases are: `PROVISIONING`,
`RUNNING`, `STOPPED`, `ERROR`, `DELETED`, `DELETING`, `PAUSED`, `STOPPING`.

kweb VM status strings vary by backend. The table below shows the mapping
for **libvirt/KVM** (the primary target for v1):

| DCM Status | kweb VM Status (libvirt) | Description |
|------------|--------------------------|-------------|
| PROVISIONING | `down` (recently created) | VM is booting after creation |
| RUNNING | `up` | VM is running |
| STOPPED | `down` (not recently created) | VM is stopped |
| PAUSED | `paused` | VM is paused (libvirt suspend) |
| ERROR | `error`, `crashed`, `nostate` | VM is in error state |
| DELETING | (transient during delete) | SP has issued delete to kweb |
| DELETED | Not found in kweb | VM has been deleted |
| STOPPING | `shuttingdown` | VM is shutting down |

The distinction between PROVISIONING and STOPPED for `down` VMs uses the
**creation timestamp** from the internal state store: if the VM was created
within the last N minutes (configurable, default 10) and is `down`, it is
reported as `PROVISIONING`; otherwise, `STOPPED`.

**Other backends:** vSphere adds `suspended` (mapped to `PAUSED`);
OpenStack may report `error` (mapped to `ERROR`). Cloud backends (AWS, GCP,
Azure) use provider-specific strings that require per-backend mapping
tables. For v1, only the **libvirt** mapping is fully specified. Other
backends will be added as they are tested and validated.

#### Cluster Status Mapping

The canonical cluster lifecycle phases are: `CREATING`, `ACTIVE`,
`UPDATING`, `DEGRADED`, `DELETED`.

| DCM Status | kweb Cluster Condition | Description |
|------------|------------------------|-------------|
| CREATING | Recently created, no nodes ready | Cluster is being provisioned |
| ACTIVE | Nodes and version present in status | Cluster is operational |
| DEGRADED | Partial node readiness | Some nodes unhealthy |
| DELETED | Not found in kweb | Cluster has been deleted |

Note: `UPDATING` is not supported in v1 because kweb does not expose cluster
update operations. The SP maps the canonical `FAILED` concept (creation
errors) to `DEGRADED` when partial state exists, or publishes a `DELETED`
event and removes the resource if creation fails completely before any nodes
come up.

### Risks and Mitigations

#### kweb Has No Authentication

**Risk:** kweb exposes full lifecycle operations without any authentication.
A network-accessible kweb instance can be used to create or destroy
infrastructure by anyone who can reach it. Additionally, kweb exposes
cluster-admin kubeconfigs via `GET /kubes/{name}/kubeconfig` without any
access control.

**Mitigation:** The homelab/dev/test deployment model assumes a **trusted
network** (local LAN, localhost, or isolated CI environment). This is
acceptable for the intended use case — a developer's workstation or a
personal server. For deployments on shared or untrusted networks, kweb
should be placed behind a reverse proxy (Nginx, Caddy, etc.) with
authentication enabled.

#### kweb Credential Exposure

**Risk:** kweb's `GET /kubes/{name}/kubeconfig` returns raw cluster-admin
kubeconfigs without authentication. The `/vmconsole/{name}` endpoint returns
VNC/SPICE passwords.

**Mitigation:** On a trusted homelab network, the kubeconfig data is no
more exposed than it would be via direct `kcli` CLI access. The SP does not
proxy the kubeconfig endpoint in v1.

#### kweb Concurrency Limitations

**Risk:** kweb's cluster creation handler spawns unbounded Python threads
(one per `POST /kubes` request). Concurrent operations may conflict on
shared kcli configuration files.

**Mitigation:** At homelab scale (a handful of concurrent operations), this
is unlikely to cause issues. The SP still serializes cluster creation
operations as a defensive measure, but this is an implementation detail
rather than a critical safeguard.

#### kweb Error Response Inconsistency

**Risk:** kweb returns errors in mixed formats — sometimes JSON with
`result`/`reason` fields, sometimes plain strings, sometimes bare HTTP
status codes. This makes error handling in the Go client fragile.

**Mitigation:** The SP's kweb HTTP client layer normalizes all responses. It
attempts JSON parsing first; on failure, wraps the raw body in a structured
error. Integration tests cover each error variant per endpoint.

#### kweb OpenAPI Spec Drift

**Risk:** The kweb `swagger.yml` has known mismatches with the actual code:
plan paths use singular instead of plural, container paths are inconsistent,
and the spec declares `PUT` for updates while the code registers a
non-standard `UPDATE` HTTP verb (which most HTTP clients cannot send).

**Mitigation:** The SP does not rely on the swagger spec for client
generation. Instead, it uses a hand-written HTTP client that targets the
verified endpoints. The SP's integration test suite validates each endpoint
against a running kweb instance.

#### Single-Tenant kweb

**Risk:** Each kweb process is bound to a single kcli configuration context
(one backend, one set of credentials). DCM environments with multiple
backends would need multiple kweb + SP pairs.

**Mitigation:** This is an architectural constraint, not a bug. Each
deployment pair (kweb + kcli SP) manages one environment. DCM's
multi-provider architecture already supports multiple providers of the same
service type; an admin registers one `kcli-vm-libvirt` and one
`kcli-vm-vsphere` as separate providers.

#### Polling Latency vs. Informer-Based Providers

**Risk:** Other DCM providers use Kubernetes informers for near-real-time
status updates. The kcli SP uses polling, introducing up to one poll-interval
of latency in status reporting.

**Mitigation:** For development and homelab use, 30 seconds of latency is
a non-issue — VM and cluster lifecycle events take minutes, not seconds.
The interval is configurable for tighter feedback in automated testing.

#### State Store Loss

**Risk:** If the persistent state store is lost (disk failure, container
restart without persistent volume), the SP loses the mapping between DCM
instance IDs and kcli resource names.

**Mitigation:** At homelab scale, this is a minor inconvenience rather than
a disaster. The SP logs a warning on startup if the store is empty but kweb
reports existing resources. Recovery means re-provisioning or manually
re-associating a handful of resources.

## Design Details

### Component Architecture

```
┌────────────────────────────────────────────────────────┐
│                  dcm-kcli-provider                      │
│                                                        │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐ │
│  │  VM Handler  │  │Cluster Handler│  │Health Handler│ │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘ │
│         │                 │                  │         │
│  ┌──────▼─────────────────▼──────────────────▼───────┐ │
│  │               kweb HTTP Client                    │ │
│  │  (error normalization, retry, rate limiting)      │ │
│  └──────────────────────┬────────────────────────────┘ │
│                         │                              │
│  ┌──────────────────────▼────────────────────────────┐ │
│  │        Persistent State Store (bbolt)              │ │
│  │  (dcm-instance-id ↔ kcli name mapping)           │ │
│  └──────────────────────┬────────────────────────────┘ │
│                         │                              │
│  ┌──────────────┐  ┌────▼─────────┐  ┌──────────────┐ │
│  │  SPM Client  │  │Status Monitor│  │ NATS Client  │ │
│  │(registration)│  │(poller+dbnce)│  │  (events)    │ │
│  └──────────────┘  └──────────────┘  └──────────────┘ │
└────────────────────────────────────────────────────────┘
         │                  │                 │
         ▼                  ▼                 ▼
   DCM SP Manager      kweb (kcli)     NATS Server
```

### Internal Packages

| Package | Responsibility |
|---------|----------------|
| `cmd/dcm-kcli-provider` | Entry point, config loading, wiring |
| `internal/config` | Environment variable parsing |
| `internal/handlers/v1alpha1` | HTTP handlers for VMs, clusters |
| `internal/handlers` | Health handler (root `/health`) |
| `internal/kweb` | kweb HTTP client, error normalization, rate limiting |
| `internal/store` | bbolt persistent state store |
| `internal/monitor` | Polling-based status monitor with debounce |
| `internal/events` | NATS CloudEvents publisher |
| `internal/registration` | Dual SPM registration (VM + cluster) |
| `api/v1alpha1` | OpenAPI spec and generated types |
| `pkg/client` | Generated HTTP client for consumers |

### Test Plan

- **Unit tests:** Each internal package has unit tests. The kweb client is
  tested against a mock HTTP server that reproduces kweb's response
  patterns, including inconsistent error formats (JSON, plain strings,
  empty bodies).
- **Integration tests:** A test suite runs against a real kweb instance
  (using libvirt with QEMU in session mode, no root required). Tests cover
  the full VM and cluster lifecycle, including error paths and status
  mapping.
- **E2E tests:** Deploy the SP alongside a kweb instance and a DCM control
  plane (using the docker-compose profile). Verify registration, CRUD
  operations, health check polling, and status reporting through the DCM
  API gateway.
- **State store tests:** Verify store persistence across SP restarts,
  orphan detection, and behavior on store loss.

### Upgrade / Downgrade Strategy

The kcli SP is stateless except for the persistent bbolt state store.

On upgrade:

- The new binary reads the existing store and resumes tracking.
- If the store schema changes, a migration step is included in the release
  notes and executed automatically on startup.

On downgrade:

- If the store schema is forward-compatible, no action is needed.
- If not, the SP reconstructs the mapping by listing all resources from
  kweb. Resources created by the newer version that cannot be mapped are
  logged as orphans.

## Implementation History

- 2026-04-17: Initial enhancement proposal.
- 2026-04-22: Enhanced with adversarial review findings: added Open
  Questions, User Stories, registration validation, per-endpoint error
  handling, pagination (AEP-132), canonical status enums, canonical
  CloudEvents format, debounce logic, persistent state store design,
  kweb security audit findings, and operational scalability bounds.
- 2026-04-22: Scoped to development/testing/homelab use. Simplified
  security posture (trusted network assumed), removed multi-replica and
  scalability ceiling concerns, updated user stories and motivation.

## Drawbacks

- **Polling instead of watching:** Unlike Kubernetes-based SPs that use
  informers for near-real-time status updates, the kcli SP must poll kweb.
  At homelab scale this is negligible, but it means status changes are
  reported with up to 30 seconds of delay.

- **External dependency on kweb:** The SP cannot function without a running
  kweb instance. This adds one more process to run alongside the SP,
  though kcli is easy to install and `kcli start kweb` is a single command.

- **No authentication in kweb:** kweb has no built-in auth. On a trusted
  homelab network this is acceptable; on shared networks it requires a
  reverse proxy. See Risks and Mitigations.

- **Name-based vs. ID-based resources:** kcli uses names as primary
  identifiers; DCM uses UUIDs. The SP must maintain a persistent mapping
  between the two. At homelab scale, losing this mapping is a minor
  inconvenience (re-provision a few resources), not a catastrophic failure.

- **Not for production:** This SP is intentionally scoped to
  development/testing/homelab. It lacks the HA, security hardening, and
  scalability guarantees of the KubeVirt and ACM providers.

- **Backend-specific status mapping:** kweb returns different status strings
  per backend. Only the libvirt mapping is fully specified for v1; other
  backends will require additional mapping tables and testing.

## Alternatives

### Alternative 1: Go Wrapper Around kcli CLI

#### Description

Instead of calling kweb's HTTP API, the SP would shell out to the `kcli`
command-line tool using `os/exec`. Each DCM operation would be translated
into one or more `kcli` CLI invocations, and the output would be parsed to
extract results.

#### Pros

- Full kcli feature coverage (the CLI exposes more functionality than kweb).
- CLI is the most stable and well-tested kcli interface.
- No dependency on a running kweb process.

#### Cons

- **Breaks DCM provider conventions.** No existing DCM SP shells out to a
  CLI. All use structured API clients (Kubernetes client-go, HTTP).
- **Container image bloat.** The Go binary would need to be packaged
  alongside a full Python runtime, libvirt client libraries, and all of
  kcli's transitive dependencies. Existing DCM SPs are lean,
  statically-compiled Go binaries in UBI-minimal images.
- **Fragile output parsing.** The CLI is designed for human consumption.
  Extracting structured data from text output is brittle and breaks across
  kcli versions without warning.
- **No structured error handling.** Error detection relies on exit codes and
  stderr string matching rather than typed error responses.
- **Testing difficulty.** Mocking `os/exec` is far more complex than mocking
  an HTTP client.
- **Subprocess overhead.** Each DCM operation spawns a Python process, adding
  latency and resource consumption.

#### Status

Rejected

#### Rationale

The CLI wrapper approach introduces significant operational and maintenance
burden without providing benefits that justify deviating from the established
DCM provider architecture. The kweb HTTP API covers the required VM and
cluster lifecycle operations, and its limitations (no auth, inconsistent
errors, no watch) are all addressable through well-understood mitigation
patterns (reverse proxy, error normalization, polling).

### Alternative 2: Python-based SP Using kcli as a Library

#### Description

Write the DCM SP in Python instead of Go, importing kcli as a Python library
(`from kvirt import Kvirt`). This would bypass both the CLI and kweb, calling
kcli's internal functions directly.

#### Pros

- Direct access to all kcli internals — no API surface limitations.
- No need for a separate kweb process.
- Maximum feature coverage with minimal translation layer.

#### Cons

- **Ecosystem mismatch.** All DCM SPs are Go binaries using oapi-codegen,
  Chi, and shared workflows. A Python SP would be an outlier requiring
  separate CI/CD, container build, and dependency management.
- **No OpenAPI codegen.** The existing Go toolchain (oapi-codegen) would not
  apply.
- **Tight coupling to kcli internals.** kcli's Python API is not versioned
  or documented as a public interface. Internal refactoring in kcli could
  break the SP without notice.
- **Deployment complexity.** The container image would need the full kcli
  dependency tree, including libvirt bindings, cloud SDK clients, and
  potentially compiled C extensions.

#### Status

Rejected

#### Rationale

The architectural consistency of the DCM ecosystem (Go, OpenAPI, Chi, shared
CI) takes precedence over the convenience of direct library access. The kweb
HTTP API provides sufficient coverage for v1 operations, and maintaining the
Go + HTTP pattern keeps the SP compatible with DCM's shared tooling,
container build pipeline, and operational model.

### Alternative 3: Contribute a gRPC/REST API to kcli Upstream

#### Description

Instead of relying on kweb's current HTTP API, contribute a new,
well-designed REST or gRPC API to the kcli project that addresses kweb's
limitations (authentication, consistent error format, watch/stream support,
OpenAPI spec accuracy).

#### Pros

- Clean API contract designed for machine-to-machine communication.
- Authentication and streaming built in from the start.
- Benefits the broader kcli community, not just DCM.

#### Cons

- **Timeline.** Upstream contribution, review, and acceptance is a
  multi-month process that cannot be gated on DCM's delivery schedule.
- **Maintenance burden.** Would require ongoing engagement with the kcli
  project to maintain the API across kcli releases.
- **Scope creep.** Designing a general-purpose API for kcli is a much larger
  effort than building a DCM-specific SP.

#### Status

Deferred

#### Rationale

This is valuable long-term work that should happen in parallel with — not
instead of — the initial SP implementation. The kcli SP can launch using kweb
as-is, with the reverse proxy and polling mitigations. If a better upstream
API materializes later, the SP's kweb client layer can be swapped with
minimal changes to the rest of the codebase.

## Infrastructure Needed

- **New repository:** `github.com/pgarciaq/dcm-kcli-provider` (created).
  May move to `github.com/dcm-project/dcm-kcli-provider` after review.
- **CI/CD:** GitHub Actions using DCM shared-workflows for CI, linting,
  OpenAPI validation, and container image builds.
- **Container registry:** `quay.io/dcm-project/dcm-kcli-provider` (once
  accepted into dcm-project org).
- **Test infrastructure:** A CI environment with libvirt/QEMU available for
  integration tests (nested virt or bare-metal runners), or a pre-deployed
  kweb instance. A developer's laptop with libvirt is sufficient for local
  testing.
