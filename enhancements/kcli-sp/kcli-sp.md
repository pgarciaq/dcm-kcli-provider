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
---

# kcli Service Provider

## Summary

The kcli Service Provider is a DCM Service Provider that manages virtual
machines and Kubernetes clusters through
[kcli](https://github.com/karmab/kcli)'s HTTP API (kweb). Unlike the existing
KubeVirt SP and ACM Cluster SP — which interact directly with Kubernetes CRDs
on a management cluster — the kcli SP communicates with a standalone kweb
instance, enabling DCM to provision infrastructure on any hypervisor or cloud
backend that kcli supports (libvirt/KVM, oVirt/RHV, vSphere, OpenStack, AWS,
GCP, Azure, IBM Cloud, and others).

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
for environments where:

1. Infrastructure is managed by traditional hypervisors (libvirt/KVM, vSphere,
   oVirt) without a Kubernetes management cluster.
2. Operators want a single tool to provision both VMs and Kubernetes clusters
   across heterogeneous infrastructure.
3. Edge or sovereign cloud deployments use lightweight infrastructure that
   doesn't justify a full Kubernetes management layer.

kcli fills this gap. It is a mature, open-source tool that wraps libvirt,
oVirt, vSphere, OpenStack, and multiple public clouds behind a unified API.
Its HTTP interface (kweb) exposes both VM and Kubernetes cluster lifecycle
operations, making it a natural fit as a DCM backend.

### Goals

- Define the lifecycle of a DCM SP that manages VMs and Kubernetes clusters
  through kweb.
- Define the dual registration flow (one per service type) with the DCM SP
  API.
- Define CREATE, READ, and DELETE endpoints for both VMs and clusters.
- Define status reporting for DCM requests.
- Define the kweb HTTP client contract and error normalization strategy.

### Non-Goals

- Day 2 operations (stop, start, restart, scale, snapshot, migrate) for VMs
  or clusters.
- Adding authentication to kweb itself (handled externally via reverse proxy
  or upstream contribution).
- Implementing a kweb instance manager or deployer; kweb is assumed to be
  pre-deployed.
- Supporting kcli plans, products, containers, networks, pools, or repos
  through DCM.
- Supporting kcli's CLI directly (this SP uses the HTTP API exclusively).
- Defining the UPDATE endpoint (out of scope for v1).

## Proposal

### Assumptions

- A kweb instance is deployed, running, and reachable over the network from
  the kcli SP binary.
- The kweb instance has valid credentials for its configured backend (e.g.,
  libvirt socket access, vSphere credentials, cloud API keys).
- kweb is deployed behind a reverse proxy or network policy that provides
  authentication and TLS termination. kweb itself has no built-in
  authentication.
- The DCM Service Provider Registry is reachable for registration.
- The DCM messaging system (NATS) is reachable for status reporting.
- Each kweb instance manages a single backend environment. Multi-environment
  setups require one kweb + one kcli SP instance per environment.

### Integration Points

#### kweb Integration

The kcli SP communicates with kweb over HTTP using a generated Go client or a
hand-written thin client. The relevant kweb endpoints are:

**VM operations:**

| Method | kweb Endpoint | Purpose |
|--------|---------------|---------|
| POST | `/vms` | Create a VM |
| GET | `/vms` | List all VMs |
| GET | `/vms/{name}` | Get VM details |
| DELETE | `/vms/{name}` | Delete a VM |
| POST | `/vms/{name}/start` | Start a VM |
| POST | `/vms/{name}/stop` | Stop a VM |

**Cluster operations:**

| Method | kweb Endpoint | Purpose |
|--------|---------------|---------|
| POST | `/kubes` | Create a cluster (async) |
| GET | `/kubes` | List all clusters |
| GET | `/kubes/{name}` | Get cluster status |
| DELETE | `/kubes/{name}` | Delete a cluster |
| GET | `/kubes/{name}/kubeconfig` | Retrieve kubeconfig |

**Health probing:**

| Method | kweb Endpoint | Purpose |
|--------|---------------|---------|
| GET | `/host` | Backend connectivity check |

kweb returns JSON responses. VM creation is synchronous; cluster creation is
asynchronous (kweb spawns a background thread and returns immediately). The SP
must poll `GET /kubes/{name}` to track cluster provisioning progress.

kweb's error responses are inconsistent: some endpoints return
`{"result": "failure", "reason": "..."}` while others return plain strings or
HTTP status codes without a JSON body. The SP's kweb client layer must
normalize all error responses into a consistent internal representation.

#### DCM SP Registry

Auto-registration on startup with DCM SP Registrar. The kcli SP registers
**twice** — once for each service type. See documentation for
[DCM Registration Flow](https://github.com/dcm-project/enhancements/blob/main/enhancements/sp-registration-flow/sp-registration-flow.md).

#### DCM SP Health Check

The kcli SP must expose a health endpoint
`http://<provider-ip>:<port>/health` for DCM control plane to poll. The health
check verifies:

1. The SP process is running (implicit from responding to HTTP).
2. kweb is reachable (the SP calls `GET /host` on kweb and checks for a valid
   response).

See documentation for
[SP Health Check](https://github.com/dcm-project/enhancements/blob/main/enhancements/service-provider-health-check/service-provider-health-check.md).

#### DCM SP Status Reporting

Publish status updates for VM and cluster instances to the messaging system
using CloudEvents format. Events are published to subjects:

- VMs: `dcm.providers.{providerName}.vm.instances.{instanceId}.status`
- Clusters:
  `dcm.providers.{providerName}.cluster.instances.{instanceId}.status`

See documentation for
[SP Status Reporting](https://github.com/dcm-project/enhancements/blob/main/enhancements/state-management/service-provider-status-reporting.md).

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

#### Registration Process

1. The SP binary starts and initializes the HTTP listener.
2. After the server is ready (via `WithOnReady` callback), registration runs
   in background goroutines — one for VMs, one for clusters.
3. Each registration request is sent to the DCM Service Provider Registry.
4. On success, the SP is registered and available for DCM to route requests.
5. Registration failures are retried with exponential backoff. Failures do not
   block server startup.
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
| DELETE | /api/v1alpha1/clusters/{clusterId} | Delete a cluster instance |

#### Common Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | /api/v1alpha1/health | SP health check |

##### AEP Compliance

All endpoints are defined based on AEP standards and use
`aep-openapi-linter` to check for compliance.

#### POST /api/v1alpha1/vms — Create a VM

The POST endpoint follows the contract defined in the VM schema spec
pre-defined by DCM core. The SP translates the DCM VM request into a kweb
`POST /vms` call.

The SP generates a `dcm-instance-id` UUID for tracking and includes it in its
internal state. Since kweb does not support arbitrary labels or metadata on
VMs, the SP maintains an internal mapping between `dcm-instance-id` and kcli
VM names.

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

The SP translates this into a kweb-compatible request:

```json
{
  "name": "web-server",
  "memory": 4096,
  "numcpus": 2,
  "image": "fedora-39",
  "keys": ["ssh-ed25519 AAAAC3..."]
}
```

#### POST /api/v1alpha1/clusters — Create a Cluster

The SP translates the DCM cluster request into a kweb `POST /kubes` call.
Cluster creation is asynchronous on the kweb side; the SP returns
`PROVISIONING` immediately and polls kweb for completion.

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
  "status": "PROVISIONING"
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

#### GET /api/v1alpha1/vms/{vmId}

Returns detailed VM information. The SP calls `GET /vms/{name}` on kweb,
maps the response to the DCM VM schema, and enriches it with the
`dcm-instance-id`.

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

#### GET /api/v1alpha1/clusters/{clusterId}

Returns cluster status. The SP calls `GET /kubes/{name}` on kweb and maps the
response to the DCM cluster schema. If the cluster is ready, the kubeconfig
is also available via `GET /kubes/{name}/kubeconfig`.

Example response payload:

```json
{
  "id": "456e7890-e89b-12d3-a456-426614174001",
  "name": "edge-cluster",
  "status": "RUNNING",
  "nodes": "3",
  "version": "v1.30.2+k3s1"
}
```

#### DELETE /api/v1alpha1/vms/{vmId}

Deletes a VM. The SP resolves the `dcm-instance-id` to a kcli VM name and
calls `DELETE /vms/{name}` on kweb. Returns `204 No Content`.

#### DELETE /api/v1alpha1/clusters/{clusterId}

Deletes a cluster and all its associated VMs. The SP resolves the
`dcm-instance-id` to a kcli cluster name and calls `DELETE /kubes/{name}` on
kweb. Returns `204 No Content`.

#### GET /api/v1alpha1/health

Returns health status. The SP probes kweb's `GET /host` endpoint to verify
backend connectivity.

Example response payload:

```json
{
  "status": "healthy",
  "version": "0.1.0",
  "uptime": "2h15m",
  "kweb": {
    "reachable": true,
    "backend": "libvirt"
  }
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
| `POLL_INTERVAL` | No | `30s` | Interval for polling kweb for status changes |
| `LOG_LEVEL` | No | `info` | Log verbosity (debug, info, warn, error) |

### Status Reporting to DCM

Since kweb does not provide a watch/informer mechanism, the kcli SP uses a
**polling-based status monitor** instead of the Kubernetes informer pattern
used by other DCM providers.

#### Polling Architecture

The SP runs a background goroutine that periodically:

1. Calls `GET /vms` and `GET /kubes` on kweb.
2. Compares the current state with the last known state.
3. For any resource whose status has changed, publishes a CloudEvents status
   update to NATS.

The poll interval is configurable (default 30 seconds). This is a trade-off:
shorter intervals increase load on kweb but reduce status reporting latency.

#### Internal State Store

The SP maintains an in-memory mapping of:

- `dcm-instance-id` → kcli resource name (VM or cluster)
- `dcm-instance-id` → last known status
- `dcm-instance-id` → resource type (`vm` or `cluster`)

This store is required because kweb has no concept of DCM instance IDs. The
SP is the translation layer between DCM's UUID-based resource model and
kcli's name-based model.

On restart, the SP reconstructs this mapping by listing all resources from
kweb and matching them against a persistent store (e.g., a local SQLite
database or a file-based store). Resources that cannot be matched are logged
as orphans.

#### CloudEvents Format

Status updates are published to NATS using the CloudEvents specification
(v1.0).

**VM events:**

```golang
event := cloudevents.NewEvent()
event.SetID("event-uuid")
event.SetSource("kcli-vm")
event.SetType("dcm.providers.kcli-vm.status.update")
event.SetSubject(
    "dcm.providers.kcli-vm.vm.instances.{instanceId}.status",
)
event.SetData(cloudevents.ApplicationJSON, VMStatus{
    Status:  "RUNNING",
    Message: "VM is running at 192.168.122.45",
})
```

**Cluster events:**

```golang
event := cloudevents.NewEvent()
event.SetID("event-uuid")
event.SetSource("kcli-cluster")
event.SetType("dcm.providers.kcli-cluster.status.update")
event.SetSubject(
    "dcm.providers.kcli-cluster.cluster.instances.{instanceId}.status",
)
event.SetData(cloudevents.ApplicationJSON, ClusterStatus{
    Status:  "RUNNING",
    Message: "Cluster is ready with 3 nodes",
})
```

#### VM Status Mapping

| DCM Status | kweb VM Status | Description |
|------------|----------------|-------------|
| PROVISIONING | `down` (recently created) | VM is being provisioned |
| RUNNING | `up` | VM is running |
| STOPPED | `down` (not recently created) | VM is stopped |
| FAILED | `error` or unreachable | VM is in error state |
| DELETED | Not found in kweb | VM has been deleted |

The distinction between PROVISIONING and STOPPED for `down` VMs is
time-based: if the VM was created within the last N minutes (configurable)
and is `down`, it is considered PROVISIONING; otherwise, STOPPED.

#### Cluster Status Mapping

| DCM Status | kweb Cluster Condition | Description |
|------------|------------------------|-------------|
| PROVISIONING | Recently created, no nodes ready | Cluster is being created |
| RUNNING | Nodes and version present | Cluster is operational |
| FAILED | Creation failed or error response | Cluster creation failed |
| DELETED | Not found in kweb | Cluster has been deleted |

### Risks and Mitigations

#### kweb Has No Authentication

**Risk:** kweb exposes full lifecycle operations without any authentication.
A network-accessible kweb instance can be used to create or destroy
infrastructure by anyone who can reach it.

**Mitigation:** Deploy kweb behind a reverse proxy (e.g., Envoy, Nginx,
HAProxy) that provides mTLS or token-based authentication. Document this as a
hard requirement in the deployment guide. Alternatively, contribute
authentication support to kcli upstream.

#### kweb Error Response Inconsistency

**Risk:** kweb returns errors in mixed formats — sometimes JSON with
`result`/`reason` fields, sometimes plain strings, sometimes bare HTTP status
codes. This makes error handling in the Go client fragile.

**Mitigation:** The SP's kweb HTTP client layer normalizes all responses. It
attempts JSON parsing first; on failure, wraps the raw body in a structured
error. Integration tests cover each error variant.

#### kweb OpenAPI Spec Drift

**Risk:** The kweb `swagger.yml` has known mismatches with the actual code:
plan paths use singular instead of plural, container paths are inconsistent,
and the spec declares `PUT` for updates while the code registers a
non-standard `UPDATE` verb.

**Mitigation:** The SP does not rely on the swagger spec for client
generation. Instead, it uses a hand-written HTTP client that targets the
verified endpoints. The SP's integration test suite validates each endpoint
against a running kweb instance.

#### Single-Tenant kweb

**Risk:** Each kweb process is bound to a single kcli configuration context
(one backend, one set of credentials). DCM environments with multiple
backends would need multiple kweb + SP pairs.

**Mitigation:** This is an architectural constraint, not a bug. Each
deployment pair (kweb + kcli SP) manages one environment. DCM's multi-provider
architecture already supports multiple providers of the same service type; an
admin registers one `kcli-vm-libvirt` and one `kcli-vm-vsphere` as separate
providers.

#### Polling Latency vs. Informer-Based Providers

**Risk:** Other DCM providers use Kubernetes informers for near-real-time
status updates. The kcli SP uses polling, introducing up to one poll-interval
of latency in status reporting.

**Mitigation:** Default poll interval is 30 seconds, which is acceptable for
VM and cluster lifecycle events (which are measured in minutes, not seconds).
The interval is configurable for deployments that need tighter feedback loops.
Future work could add a kweb webhook/SSE endpoint upstream to enable
push-based status.

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
│  │  (error normalization, retry, timeout)            │ │
│  └──────────────────────┬────────────────────────────┘ │
│                         │                              │
│  ┌──────────────────────▼────────────────────────────┐ │
│  │             Internal State Store                  │ │
│  │  (dcm-instance-id ↔ kcli name mapping)           │ │
│  └──────────────────────┬────────────────────────────┘ │
│                         │                              │
│  ┌──────────────┐  ┌────▼─────────┐  ┌──────────────┐ │
│  │  SPM Client  │  │Status Monitor│  │ NATS Client  │ │
│  │(registration)│  │  (poller)    │  │  (events)    │ │
│  └──────────────┘  └──────────────┘  └──────────────┘ │
└────────────────────────────────────────────────────────┘
         │                  │                 │
         ▼                  ▼                 ▼
   DCM SP Manager      kweb (kcli)     NATS Server
```

### Internal Packages

| Package | Responsibility |
|---------|----------------|
| `cmd/dcm-kcli-provider` | Process entry point, config loading, wiring |
| `internal/config` | Environment variable parsing |
| `internal/handlers/v1alpha1` | HTTP request handlers for VMs, clusters, health |
| `internal/kweb` | kweb HTTP client with error normalization |
| `internal/store` | In-memory + persistent state store |
| `internal/monitor` | Polling-based status monitor |
| `internal/events` | NATS CloudEvents publisher |
| `internal/registration` | Dual SPM registration (VM + cluster) |
| `api/v1alpha1` | OpenAPI spec and generated types |
| `pkg/client` | Generated HTTP client for consumers |

### Test Plan

- **Unit tests:** Each internal package has unit tests. The kweb client is
  tested against a mock HTTP server that reproduces kweb's response patterns,
  including inconsistent error formats.
- **Integration tests:** A test suite runs against a real kweb instance
  (using libvirt with QEMU in session mode, no root required). Tests cover
  the full VM and cluster lifecycle.
- **E2E tests:** Deploy the SP alongside a kweb instance and a DCM control
  plane (using the docker-compose profile). Verify registration, CRUD
  operations, and status reporting through the DCM API gateway.

### Upgrade / Downgrade Strategy

The kcli SP is stateless except for the internal instance-ID mapping store.
On upgrade:

- The new binary reads the existing persistent store and resumes tracking.
- If the store format changes, a migration step is included in the release
  notes.

On downgrade:

- If the store format is forward-compatible, no action is needed.
- If not, the SP reconstructs the mapping by listing all resources from kweb.
  Resources created by the newer version that cannot be mapped are logged as
  orphans.

## Implementation History

- 2026-04-17: Initial enhancement proposal.

## Drawbacks

- **Polling instead of watching:** Unlike Kubernetes-based SPs that use
  informers for near-real-time status updates, the kcli SP must poll kweb.
  This introduces latency and adds load to kweb proportional to the number of
  managed resources and the poll frequency.

- **External dependency on kweb:** The SP cannot function without a running
  kweb instance. This adds an operational component that must be deployed,
  monitored, and secured (via reverse proxy) alongside the SP.

- **No authentication in kweb:** kweb has no built-in auth. The SP cannot
  verify that it is talking to the intended kweb instance, and kweb cannot
  verify that requests come from the SP. This must be mitigated at the
  network/proxy layer.

- **Name-based vs. ID-based resources:** kcli uses names as primary
  identifiers; DCM uses UUIDs. The SP must maintain a mapping between the two,
  which adds state management complexity and a potential failure mode if the
  mapping is lost.

## Alternatives

### Alternative 1: Go Wrapper Around kcli CLI

#### Description

Instead of calling kweb's HTTP API, the SP would shell out to the `kcli`
command-line tool using `os/exec`. Each DCM operation would be translated into
one or more `kcli` CLI invocations, and the output would be parsed to extract
results.

#### Pros

- Full kcli feature coverage (the CLI exposes more functionality than kweb).
- CLI is the most stable and well-tested kcli interface.
- No dependency on a running kweb process.

#### Cons

- **Breaks DCM provider conventions.** No existing DCM SP shells out to a
  CLI. All use structured API clients (Kubernetes client-go, HTTP).
- **Container image bloat.** The Go binary would need to be packaged alongside
  a full Python runtime, libvirt client libraries, and all of kcli's
  transitive dependencies. Existing DCM SPs are lean, statically-compiled Go
  binaries in UBI-minimal images.
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
- **Tight coupling to kcli internals.** kcli's Python API is not versioned or
  documented as a public interface. Internal refactoring in kcli could break
  the SP without notice.
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

Instead of relying on kweb's current HTTP API, contribute a new, well-designed
REST or gRPC API to the kcli project that addresses kweb's limitations
(authentication, consistent error format, watch/stream support, OpenAPI spec
accuracy).

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
as-is, with the reverse proxy and polling mitigations. If a better upstream API
materializes later, the SP's kweb client layer can be swapped with minimal
changes to the rest of the codebase.

## Infrastructure Needed

- **New repository:** `github.com/pgarciaq/dcm-kcli-provider` (created).
  May move to `github.com/dcm-project/dcm-kcli-provider` after review.
- **CI/CD:** GitHub Actions using DCM shared-workflows for CI, linting,
  OpenAPI validation, and container image builds.
- **Container registry:** `quay.io/dcm-project/dcm-kcli-provider` (once
  accepted into dcm-project org).
- **Test infrastructure:** A CI environment with libvirt/QEMU available for
  integration tests, or a pre-deployed kweb instance accessible from CI
  runners.
