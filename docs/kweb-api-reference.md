# kweb API Reference (Captured from Apollo)

Captured 2026-04-22 from `hpe-apollo-cn99xx-16.khw.eng.rdu2.dc.redhat.com` running
kcli 99.0 with a libvirt/KVM backend and one existing OpenShift SNO cluster (`sno`)
with VM `sno-sno`.

## GET /host

Returns host info as a flat JSON object. HTTP 200.

```json
{
    "connection": "qemu:///system",
    "host": "hpe-apollo-cn99xx-16.khw.eng.rdu2.dc.redhat.com",
    "vms_running": 1,
    "cpus_total": 224,
    "cpus_used": 64,
    "memory_used": 131072,
    "memory_total": 256564,
    "storage": ["storage: default, type: dir, path: /home/libvirt/images, ..."],
    "networks": ["name: enp11s0f0np0, type: bridged", "name: default, type: routed, ..."]
}
```

## GET /vms

Returns `{"vms": [array]}`. Each VM is a dict with `name`, `status`, `ip`, `nets`,
`disks`, `id`, `plan`, `profile`, `kube`, `kubetype`, `creationdate`, `info`.

```json
{
    "vms": [
        {
            "name": "sno-sno",
            "status": "up",
            "ip": "192.168.122.115",
            "id": "cce4e73d-b79a-4555-9116-4d30fc95cb4f",
            "plan": "sno",
            "profile": "kvirt",
            "kube": "sno",
            "kubetype": "openshift",
            "creationdate": "21-04-2026 08:48",
            "nets": [{"device": "eth0", "mac": "52:54:00:82:b4:08", "net": "default", "type": "routed"}],
            "disks": [],
            "info": "name: sno-sno\nid: ...\nstatus: up\n..."
        }
    ]
}
```

## GET /vms/{name}

Returns a flat dict with VM detail. Includes `numcpus`, `memory`, `autostart`, `user`,
full `disks` array, `iso` path. HTTP 200.

For nonexistent VMs: returns `{}` with HTTP 200 (not 404).

```json
{
    "name": "sno-sno",
    "status": "up",
    "ip": "192.168.122.115",
    "id": "cce4e73d-b79a-4555-9116-4d30fc95cb4f",
    "numcpus": 64,
    "memory": 131072,
    "autostart": false,
    "user": "core",
    "plan": "sno",
    "profile": "kvirt",
    "kube": "sno",
    "kubetype": "openshift",
    "creationdate": "21-04-2026 08:48",
    "nets": [{"device": "eth0", "mac": "52:54:00:82:b4:08", "net": "default", "type": "routed"}],
    "disks": [
        {"device": "vda", "size": 200, "format": "virtio", "type": "qcow2", "path": "/home/libvirt/images/sno-sno_0.img"},
        {"device": "vdb", "size": 400, "format": "virtio", "type": "qcow2", "path": "/home/libvirt/images/sno-sno_1.img"}
    ],
    "iso": "/home/libvirt/images/sno-sno.iso"
}
```

## GET /vmprofiles

Returns `{"profiles": {dict}}`. Empty dict when no profiles are configured.

```json
{"profiles": {}}
```

## GET /kubes

Returns `{"kubes": {dict of kube_name: kube_info}}`. Each kube has `type`, `plan`,
`vms` (comma-separated string of VM names).

```json
{
    "kubes": {
        "sno": {
            "type": "openshift",
            "plan": "sno",
            "vms": "sno-sno"
        }
    }
}
```

## GET /kubes/{name}

Returns cluster detail from `info_specific_kube()`. Contains `nodes` (array of arrays)
and `version` (string with embedded table row).

For nonexistent clusters: returns `{}` with HTTP 200 (not 404).

```json
{
    "nodes": [
        ["sno-sno.karmalabs.corp", "Ready", "control-plane,master,worker", "24h", "v1.34.6", "192.168.122.115"]
    ],
    "version": "version   4.21.9   True   False   24h   Cluster version is 4.21.9"
}
```

## GET /kubes/{name}/kubeconfig

Returns raw kubeconfig text (not JSON). HTTP 200.
Returns `{}` with HTTP 404 if not found.

## POST /vms

Requires `name` and `profile` in JSON body. Returns `{"result": "success"}` on HTTP 200.

Error cases:
- No body: `{"result": "failure", "reason": "Invalid Data"}` HTTP 400
- Missing `profile`: HTML 500 (Python KeyError, not JSON)
- Duplicate name: returns success but overwrites (kweb has no conflict detection)

## POST /kubes

Accepts `cluster` (name) and `type` or `kubetype`. Returns immediately (async).
- `kind` type: HTML 500 (Python UnboundLocalError)
- No body: `{"result": "failure", "reason": "Invalid Data"}` HTTP 400

## DELETE /vms/{name}

Returns `{"result": "success"}` on HTTP 200.
Nonexistent VM: HTML 500 (Python exception).

## DELETE /kubes/{name}

Returns result dict on HTTP 200.

## Key Observations

1. **Wrapper keys**: All list endpoints wrap responses in named keys (`vms`, `kubes`, `profiles`).
2. **No 404s**: kweb returns `{}` with HTTP 200 for nonexistent resources, or HTML 500 for delete.
3. **HTML errors**: Many error paths return HTML 500, not JSON. The SP must handle non-JSON error bodies.
4. **Profile is mandatory**: POST /vms crashes (500) if `profile` is missing.
5. **No conflict detection**: Creating a VM with an existing name may overwrite or error depending on libvirt.
6. **Status strings**: VMs use `up`/`down` (libvirt). Clusters have no single status field.
7. **Empty profiles**: No profiles configured on this host; profiles are user-defined in `~/.kcli/profiles.yml`.
