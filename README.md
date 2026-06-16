# kcli Service Provider for DCM

A [DCM](https://github.com/dcm-project) Service Provider that manages virtual
machines and Kubernetes clusters through
[kcli](https://github.com/karmab/kcli)'s HTTP API
([kweb](https://kcli.readthedocs.io/en/latest/#web-interface)).

**Designed for development, testing, and homelab environments.** For production
workloads, use the [KubeVirt SP](https://github.com/dcm-project/kubevirt-service-provider)
or [ACM Cluster SP](https://github.com/dcm-project/acm-cluster-service-provider).

> **This service provider is not intended for production use.** kweb has no
> authentication, no TLS, no rate limiting, and no SLA guarantees. The kcli SP
> inherits these limitations.

## Status

**Working prototype.** The provider implements VM and Cluster lifecycle
(create, get, list, delete) against kweb, with OpenAPI-first request
validation, SPM registration, NATS status events, and bbolt persistence.
See [enhancements/kcli-sp/kcli-sp.md](enhancements/kcli-sp/kcli-sp.md) for the
full design document.

## Overview

This provider registers with the DCM control plane as two separate service
types:

| Registration | Service Type | Description |
|---|---|---|
| `kcli-vm` | `vm` | Create, read, delete VMs on any kcli-supported hypervisor |
| `kcli-cluster` | `cluster` | Create, read, delete Kubernetes clusters (generic, k3s, OpenShift, MicroShift, HyperShift) |

Unlike the existing KubeVirt and ACM providers — which talk directly to
Kubernetes CRDs — this provider communicates with kcli's kweb HTTP API. This
makes it ideal for developers and homelab operators who want to manage VMs and
clusters through DCM without deploying a Kubernetes management stack.

## API

The SP exposes an OpenAPI 3.0 API under `/api/v1alpha1`. The full spec is in
[`api/v1alpha1/openapi.yaml`](api/v1alpha1/openapi.yaml).

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | /api/v1alpha1/vms?id={id} | Create a VM (`?id=` optional) |
| GET | /api/v1alpha1/vms | List VMs |
| GET | /api/v1alpha1/vms/{vmId} | Get a VM |
| DELETE | /api/v1alpha1/vms/{vmId} | Delete a VM |
| POST | /api/v1alpha1/clusters?id={id} | Create a cluster (`?id=` optional) |
| GET | /api/v1alpha1/clusters | List clusters |
| GET | /api/v1alpha1/clusters/{clusterId} | Get a cluster |
| DELETE | /api/v1alpha1/clusters/{clusterId} | Delete a cluster |
| GET | /api/v1alpha1/health | SP health check |
| GET | /api/v1alpha1/vms/health | VM service health (used by SPM) |
| GET | /api/v1alpha1/clusters/health | Cluster service health (used by SPM) |
| GET | /metrics | Prometheus metrics |

### Request Format

Create requests use a `{"spec": <CatalogSpec>}` envelope aligned with the
SPM generic resource protocol. Only `service_type` is required in the spec;
`metadata`, `guest_os`, and other fields are optional.

```bash
curl -X POST "http://localhost:8080/api/v1alpha1/vms?id=my-vm" \
  -H "Content-Type: application/json" \
  -d '{"spec":{"service_type":"vm","guest_os":{"type":"fedora41"},"memory":{"size":"2GB"}}}'
```

See [`docs/examples/`](docs/examples/) for full DCM flow examples (catalog
items, policies, and catalog-item-instances).

## Architecture

![Architecture Overview](docs/architecture-overview.svg)

## Development

### Prerequisites

- Go 1.25+
- [golangci-lint](https://golangci-lint.run/)
- [Spectral CLI](https://github.com/stoplightio/spectral) (for AEP linting)

### Building

```bash
make build
```

### Testing

```bash
make test          # run all Ginkgo suites with -race
make test-cover    # run with coverage
make lint          # golangci-lint
make check         # lint + test
```

### Code Generation

```bash
make generate-api          # regenerate types, server, client from OpenAPI spec
make check-generate-api    # verify generated files are in sync (used by CI)
make check-aep             # verify OpenAPI spec passes AEP linting (used by CI)
```

### Local Development with Docker Compose

```bash
docker compose up --build
```

### Releasing

Container images are pushed to
[`quay.io/pgarciaq/dcm-kcli-provider`](https://quay.io/repository/pgarciaq/dcm-kcli-provider)
on version tags (`v*`). Manual builds can be triggered via `workflow_dispatch`.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.
