# kcli SP Enhancement Proposal — Adversarial Due Diligence Review

Comprehensive review covering security, correctness, auditability,
operational robustness, performance, design quality, maintainability, and
governance. Findings cross-referenced against kweb source code
(`kvirt/web/__init__.py`, `kvirt/web/swagger.yml`), DCM enhancement template,
canonical DCM contracts (health-check, status-reporting, registration-flow),
and peer SP proposals (KubeVirt, K8s Container).

**Severity summary:** 2 Critical, 6 High, 12 Medium, 5 Low, 1 Info — 26
findings total.

> **Note:** All findings from this review have been addressed in the
> enhanced proposal (commit `a119447`). This document is preserved as an
> audit trail.

> **Scope update (2026-04-22):** The proposal has been rescoped to
> **development, testing, and homelab environments only** (trusted networks,
> small scale, single instance). This significantly changes the effective
> severity of several findings:
>
> - **SEC-01, SEC-02 (Critical → Low for homelab):** kweb's lack of auth is
>   acceptable on a trusted homelab LAN — no different from running `virsh`
>   or `kubectl` without auth on localhost.
> - **OPS-01 (High → Low):** State store loss at homelab scale means
>   re-provisioning a handful of resources, not a production incident.
> - **OPS-02 (Medium → N/A):** Multi-replica is not needed for dev/homelab.
> - **OPS-03 (Medium → N/A):** Polling scalability is irrelevant with tens
>   of resources.
> - **OPS-04 (Medium → Low):** kweb concurrency issues are unlikely with
>   single-digit concurrent operations.
>
> The original severity ratings are preserved below for reference if the
> scope is ever expanded to production use.

---

## Security

> **kweb is equivalent to root on the virtualization control plane.**
> Without authentication, TLS, or network isolation, kweb grants full
> lifecycle control (create, delete, console, kubeconfig) to any network
> client. The proposal acknowledges this but originally classified the
> mitigation as an assumption rather than a hard gate. A compromised or
> misconfigured network path exposes all managed infrastructure.

| ID | Finding | Severity | Evidence | Proposal Gap |
|----|---------|----------|----------|--------------|
| SEC-01 | kweb has zero authentication | Critical | Any network client can create/delete VMs and clusters. Kubeconfigs (cluster-admin) are downloadable without auth. | Proposal mentions reverse proxy mitigation but does not require mTLS or network policy as a hard prerequisite in Assumptions. |
| SEC-02 | Kubeconfig credential exposure | Critical | `GET /kubes/{name}/kubeconfig` returns raw cluster-admin kubeconfig to any caller. Not gated by kweb readonly mode. | Not mentioned in the proposal. The SP will proxy this data; if the SP-to-kweb link is compromised, all managed clusters are compromised. |
| SEC-03 | os.popen command execution in kweb | High | vmconsole handler runs websockify via `os.popen` with values derived from provider metadata. Potential command injection vector. | Not relevant to v1 SP (no console proxy), but should be documented as a kweb risk for deployment hardening guidance. |
| SEC-04 | VNC/SPICE password leak | High | vmconsole returns VNC/SPICE passwords in JSON. Combined with no auth, this is sensitive data exposure. | Out of scope for SP, but needs mention in deployment security guidance. |
| SEC-05 | Open redirect in vmconsole | High | Non-VNC/SPICE console URLs trigger `redirect(consoleurl)` — classic open redirect if URL is attacker-influenced. | Out of scope for SP, but reinforces the "kweb must be network-isolated" requirement. |

---

## Correctness

Cross-verification of every technical claim against kweb source code
(`kvirt/web/__init__.py`), `swagger.yml`, and canonical DCM enhancements.

| ID | Finding | Severity | Evidence | Action Required |
|----|---------|----------|----------|-----------------|
| COR-01 | Health endpoint path mismatch | High | Proposal defines `GET /api/v1alpha1/health`, but DCM health-check enhancement mandates `GET /health` (root path). DCM control plane probes `/health`. | Must fix: expose `/health` at root, not under API prefix. Internal inconsistency between proposal lines 149-151 and 258-260. |
| COR-02 | Cluster status enum mismatch | High | Proposal uses PROVISIONING/RUNNING/FAILED/DELETED for clusters. Canonical status-reporting doc defines CREATING/ACTIVE/UPDATING/DEGRADED/DELETED. | Must fix: align with canonical cluster status enum or explicitly document and justify the deviation. |
| COR-03 | VM status enum incomplete | Medium | Proposal maps only PROVISIONING/RUNNING/STOPPED/FAILED/DELETED. Canonical enum includes ERROR, DELETING, PAUSED, STOPPING. | Should map all canonical states. kweb's `down` status hides PAUSED vs STOPPED distinction. Document which states are unsupported and why. |
| COR-04 | VM status values are backend-dependent | Medium | Proposal claims kweb returns `up`/`down`/`error`. Verified for libvirt only. vSphere adds `suspended`; OpenStack has `error`; GCP/Azure use different strings entirely. | Status mapping table must be expanded per-backend or documented as libvirt-only for v1. |
| COR-05 | POST /vms requires `profile` field | Medium | kweb vmcreate handler expects `data['profile']` (KeyError if missing). Proposal's example kweb payload uses `image` and omits `profile`. | Must fix: correct the translation example to include the profile field, or document which kweb versions accept image-only. |
| COR-06 | kweb returns 200, not 201 | Low | kweb VM/cluster create returns HTTP 200. Proposal's SP returns 201 (correct for DCM). But SP must not assume 201 from kweb. | Document that kweb success = 200 and SP translates to 201. |

---

## Template and Governance Compliance

Gap analysis against the DCM enhancement template and peer proposals.

| ID | Finding | Severity | Detail |
|----|---------|----------|--------|
| TPL-01 | Missing "Open Questions" section | Medium | Required by template. Multiple open questions exist (kweb version pinning, persistent store technology, multi-backend status mapping). |
| TPL-02 | Missing "User Stories" section | Medium | Required by template. Both KubeVirt and K8s Container proposals include user stories. Present in the canonical status-reporting doc as well. |
| TPL-03 | No pagination support (AEP-132) | Medium | K8s Container SP defines `max_page_size`/`page_token` for list endpoints. kcli proposal has no pagination story for `GET /vms` or `GET /clusters`. |
| TPL-04 | No per-endpoint error responses | Medium | K8s Container documents 400/404/409/500 per endpoint. kcli proposal has no error response specification. |
| TPL-05 | No registration validation subsection | Low | K8s Container SP has explicit validation rules (`serviceType` must be `container`, minimum operations). kcli should specify equivalent rules. |
| TPL-06 | Registration API version ambiguity | Low | Proposal uses `POST /api/v1alpha1/providers`. KubeVirt and sp-registration-flow examples use `/api/v1/providers`. Should state which version and why. |

---

## Operational Robustness

> **State management is the critical operational risk.** Unlike
> Kubernetes-based SPs where state lives in etcd/CRDs, the kcli SP owns its
> own instance-ID mapping. Loss of this mapping orphans all managed
> resources from DCM's perspective. The proposal mentions "a local SQLite
> database or a file-based store" without committing to either or defining
> durability guarantees.

| ID | Finding | Severity | Detail | Recommendation |
|----|---------|----------|--------|----------------|
| OPS-01 | State store is a single point of failure | High | If the internal dcm-instance-id mapping is lost (disk failure, container restart without volume), all resource tracking is broken. Orphan detection by name matching is unreliable (name collisions, manual kweb usage). | Must define: what persistent store (SQLite? file? configmap?), backup strategy, and behavior on total loss. |
| OPS-02 | No multi-replica SP story | Medium | Existing K8s-based SPs can run multiple replicas because state lives in Kubernetes. kcli SP's internal state store prevents horizontal scaling unless the store is externalized (e.g., PostgreSQL, etcd). | Document as a v1 limitation or define shared store architecture. |
| OPS-03 | Polling scalability not bounded | Medium | `GET /vms` returns all VMs with enriched info (`print_info`). At 500+ VMs with 30s polling, this generates significant load on kweb and the underlying hypervisor API. | Should define a maximum managed resource count for v1, or implement incremental/filtered polling. |
| OPS-04 | kweb concurrency is unsafe | Medium | Cluster creation spawns unbounded threads. Concurrent `POST /kubes` can exhaust resources. Concurrent VM creates may conflict on shared kcli config state. | SP should serialize or rate-limit kweb requests. Document as a kweb limitation. |
| OPS-05 | No debounce for status flapping | Low | Canonical status-reporting doc and K8s Container SP both mandate debouncing. kcli proposal's poll-compare-publish has no debounce mention. | Add debounce logic between state comparison and NATS publish. |

---

## Design Quality and Maintainability

| ID | Finding | Severity | Detail | Recommendation |
|----|---------|----------|--------|----------------|
| DES-01 | NATS subject format diverges from canonical doc | Medium | Proposal uses `dcm.providers.{name}.vm.instances.{id}.status`. Canonical doc says subject = `dcm.{serviceType}`; provider/instance context goes in CloudEvent envelope. | Align with canonical format or document why K8s Container/kcli both deviate. |
| DES-02 | CloudEvent type inconsistency | Low | Proposal uses `dcm.providers.kcli-vm.status.update` (provider-name-specific). K8s Container uses `dcm.providers.{providerName}.status.update`. Canonical example uses `dcm.status.vm`. | Three different patterns across three docs. Needs ecosystem-level alignment. |
| DES-03 | PROVISIONING vs STOPPED heuristic is fragile | Medium | Time-based distinction (`down` + recently created = PROVISIONING, otherwise STOPPED) breaks if: (a) VM creation is slow, (b) VM is intentionally stopped right after creation, (c) clocks drift. | Consider tracking creation timestamps explicitly in the state store rather than relying on wall-clock heuristics. |
| DES-04 | kweb `kind` cluster type causes crash | Low | `swagger.yml` lists `kind` as a valid kubetype, but kweb code has no handler for it. `thread.start()` will fail with `UnboundLocalError`. | SP should validate cluster types before forwarding to kweb. |
| DES-05 | Kubeconfig retrieval not exposed in DCM API | Info | Proposal mentions kubeconfig availability but does not define a DCM endpoint for it. ACM Cluster SP may have a pattern to follow. | Consider adding `GET /api/v1alpha1/clusters/{id}/kubeconfig` or document as future work. |

---

## Verified Claims

The following proposal claims were verified against kweb source code and
confirmed accurate.

| Claim | Verdict | Notes |
|-------|---------|-------|
| All listed kweb endpoints exist | Verified | POST/GET/DELETE for `/vms` and `/kubes` confirmed in code and swagger |
| HTTP methods are correct | Verified | Including `DELETE /vms/{name}` and `DELETE /kubes/{name}` |
| VM creation is synchronous | Verified | No threading in vmcreate handler |
| Cluster creation is asynchronous | Verified | `Thread.start()` in kubecreate; returns immediately |
| kweb has no authentication | Verified | No auth middleware, tokens, or session checks |
| Error responses are inconsistent | Verified | Mix of JSON dicts, plain strings, and empty bodies |
| Cluster types: generic, k3s, openshift, microshift, hypershift | Verified | All have elif branches in kubecreate |
| Dual registration is architecturally supported | Verified | Provider model allows unique Name per ServiceType |
| kweb OpenAPI spec has drift from code | Verified | Plan paths, container paths, PUT vs UPDATE verb |

---

## Priority Actions

### Must Fix Before Merge

1. Fix health endpoint path: expose `GET /health` at root, not
   `/api/v1alpha1/health`.
2. Align cluster status enum with canonical:
   CREATING/ACTIVE/UPDATING/DEGRADED/DELETED.
3. Expand VM status mapping to cover full canonical enum (ERROR, DELETING,
   PAUSED, STOPPING).
4. Fix `POST /vms` translation example: include required `profile` field.
5. Add Open Questions section (persistent store choice, kweb version
   pinning, multi-backend status).
6. Add User Stories section per template requirement.
7. Elevate kweb security from assumption to hard prerequisite
   (mTLS/network policy required).

### Should Fix

1. Add per-endpoint error response documentation (400/404/409/500).
2. Add pagination support for list endpoints (AEP-132).
3. Define persistent state store technology and durability guarantees.
4. Add debounce logic specification for status flapping.
5. Document kweb kubeconfig exposure as a security consideration.
6. Align NATS subject format with canonical doc or justify deviation.
7. Add registration validation subsection.
8. Document single-replica limitation for v1.

---

*Review performed 2026-04-22. Sources: `kvirt/web/__init__.py`,
`kvirt/web/swagger.yml`, DCM enhancements (template, sp-registration-flow,
service-provider-health-check, service-provider-status-reporting,
kubevirt-sp, k8s-container-sp).*
