# DCM Configuration Examples

These are example DCM control-plane objects for use with the kcli service
provider. They are **not** deployed by the SP itself — they are created by the
DCM administrator after the stack is running.

## Prerequisites

- A running DCM stack (API gateway, SPM, catalog-manager, policy-manager,
  placement-manager)
- The kcli SP registered and healthy (`health_status: ready`)

## Creating the examples

All commands target the DCM API gateway (default `:9080`).

### 1. Catalog items

Catalog items define what users can provision through the DCM UI.

```bash
# VM catalog item
curl -X POST http://localhost:9080/api/v1alpha1/catalog-items \
  -H "Content-Type: application/json" \
  -d @catalog-item-vm.json

# Cluster catalog item
curl -X POST http://localhost:9080/api/v1alpha1/catalog-items \
  -H "Content-Type: application/json" \
  -d @catalog-item-cluster.json
```

### 2. Routing policy

The policy tells the placement manager which provider to use. The example
below unconditionally routes all requests to `kcli-vm`. In production you
would use Rego logic to select providers based on labels, regions, or
capacity.

```bash
curl -X POST http://localhost:9080/api/v1alpha1/policies \
  -H "Content-Type: application/json" \
  -d @policy-route-to-kcli.json
```

### 3. Create a VM through the full DCM flow

Once the catalog item and policy exist, create an instance:

```bash
curl -X POST "http://localhost:9080/api/v1alpha1/catalog-item-instances?id=my-vm" \
  -H "Content-Type: application/json" \
  -d '{
    "api_version": "v1alpha1",
    "display_name": "My Fedora VM",
    "spec": {
      "catalog_item_id": "<uid from catalog-item-vm response>",
      "user_values": [
        {"path": "guest_os.type", "value": "fedora41"},
        {"path": "memory.size", "value": "2GB"},
        {"path": "vcpu.count", "value": 2}
      ]
    }
  }'
```

In the full DCM flow, SPM sends `POST {endpoint}?id=<uuid>` with a
`{"spec": <resolved_spec>}` body to the kcli SP. The `metadata` field is
typically absent (catalog-manager does not include it unless the catalog
item defines a `metadata.name` field). The SP derives the kcli resource
name from the `?id=` parameter when `metadata.name` is missing.

### 4. Direct SP API calls (without the DCM control plane)

You can also call the kcli SP directly, bypassing catalog/placement/policy.
The `?id=` query parameter is optional — if omitted, the SP generates a UUID.
`metadata` and `guest_os` are optional; only `service_type` is required.

```bash
# Create a VM (with metadata — name is used as kcli VM name)
curl -X POST "http://localhost:8080/api/v1alpha1/vms?id=my-vm-id" \
  -H "Content-Type: application/json" \
  -d '{
    "spec": {
      "service_type": "vm",
      "metadata": {"name": "my-fedora"},
      "guest_os": {"type": "fedora-39"},
      "memory": {"size": "4GB"},
      "vcpu": {"count": 2}
    }
  }'

# Create a VM (catalog style — no metadata, name from ?id=)
curl -X POST "http://localhost:8080/api/v1alpha1/vms?id=my-catalog-vm" \
  -H "Content-Type: application/json" \
  -d '{
    "spec": {
      "service_type": "vm",
      "guest_os": {"type": "fedora41"},
      "memory": {"size": "2GB"},
      "vcpu": {"count": 2}
    }
  }'

# Create a k3s cluster (cluster_type via provider_hints)
curl -X POST "http://localhost:8080/api/v1alpha1/clusters?id=my-cluster-id" \
  -H "Content-Type: application/json" \
  -d '{
    "spec": {
      "service_type": "cluster",
      "metadata": {"name": "my-k3s"},
      "nodes": {
        "control_plane": {"count": 1},
        "workers": {"count": 2}
      },
      "provider_hints": {
        "kcli": {"cluster_type": "k3s"}
      }
    }
  }'
```

## Customization

- **OS images**: Edit the `enum` list in `catalog-item-vm.json` to match the
  images available on your kweb host (`curl http://<kweb>/vmprofiles`).
- **Provider name**: If you changed `PROVIDER_NAME_VM` or
  `PROVIDER_NAME_CLUSTER`, update the `selected_provider` value in the
  policy accordingly.
- **Conditional routing**: Replace the simple Rego rule with logic that
  inspects `input.spec` fields to route different workloads to different
  providers.
