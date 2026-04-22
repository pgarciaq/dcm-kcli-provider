# Lessons Learned: Building a DCM Service Provider

Practical patterns, antipatterns, and advice distilled from building the kcli
Service Provider — applicable to anyone writing a new DCM SP.

---

## 1. Start from the OpenAPI spec, not from code

Write `openapi.yaml` first and generate Go types and server scaffolding with
`oapi-codegen`. This gives you:

- **Request validation middleware for free.** The `nethttp-middleware` validator
  rejects malformed payloads before your handler sees them. During E2E testing,
  it caught a missing `api_version` field — our code never needed to check for
  it.
- **Type-safe request/response objects.** The `StrictServerInterface` pattern
  makes it impossible to return the wrong status code for a given response shape.
- **The spec is the documentation.** Consumers (SPM, catalog-manager) can read
  your OpenAPI spec and know exactly what you accept and return.

**Antipattern:** Hand-writing route handlers and validating request bodies
manually. You will miss edge cases that the OpenAPI validator catches
automatically.

**Concrete advice:** Use the `strict-server` generation mode. The generated
response types (`CreateVM201JSONResponse`,
`CreateVM409ApplicationProblemPlusJSONResponse`) are verbose, but they enforce
correctness at compile time.

## 2. Define interfaces at the consumer, not the provider

The `KwebClient`, `StateStore`, and `ProfileCache` interfaces in `impl.go` are
defined where they are *used*, not in the packages that implement them. This is
idiomatic Go, but it is especially important in SPs because:

- Your backend (kweb, libvirt, AWS, whatever) will have quirks. The interface
  shields your handler logic from those quirks.
- Tests become trivial — our `mocks_test.go` is ~50 lines, not 500.
- You can swap backends without touching handler code.

```go
// Defined in internal/api/server/impl.go, not in internal/kweb/
type KwebClient interface {
    CreateVM(ctx context.Context, name, profile string, params map[string]interface{}) error
    ListVMs(ctx context.Context) ([]kweb.VMInfo, error)
    GetVM(ctx context.Context, name string) (*kweb.VMInfo, error)
    DeleteVM(ctx context.Context, name string) error
    // ...
}
```

## 3. Name-prefix everything you create in the backend

Use a constant prefix on all resources you create in the backend system:

```go
const dcmPrefix = "dcm-"
kcliName := dcmPrefix + resolveVMName(spec, req.Params.Id)
```

This is critical because:

- The backend does not know about DCM. Other tools create resources too.
- The monitor needs to distinguish "ours" from "theirs" (orphan detection).
- It prevents name collisions between DCM-managed and manually-created
  resources.
- Cleanup scripts can safely target `dcm-*` resources without risk.

## 4. Your state store is the source of truth, not the backend

The SP maintains a bbolt store that maps `DCM ID → kcli name + status`. The
backend (kweb) is the system of record for *actual* resource state, but the SP's
store is the source of truth for *what DCM thinks it owns*.

**Why this matters:** When listing VMs, we read from the store first, then enrich
with live data from kweb. If kweb is down, we can still report what we know. If a
VM disappears from kweb, the monitor detects it and cleans up the store.

**The rollback pattern** in `CreateVM` is worth highlighting — if store
persistence fails after creating the VM in kweb, we roll back by deleting the VM:

```go
if err := s.store.Put(entry); err != nil {
    if delErr := s.kweb.DeleteVM(ctx, kcliName); delErr != nil {
        s.logger.Warn("rollback: failed to delete VM from kweb after store error",
            "vm", kcliName, "error", delErr)
    }
    return problemError(500, "failed to persist VM state")
}
```

## 5. The SPM contract is simple but has non-obvious requirements

### Health endpoints per service type

If your SP registers as both `vm` and `cluster`, SPM will health-check
`/vms/health` and `/clusters/health` independently. A single `/health` is not
enough. SPM marks each provider as `ready`/`not_ready` separately.

### The `?id=` query parameter for idempotent creates

SPM passes `?id=<uuid>` on POST. Your create handler *must* check this ID first
and return the existing resource if it already exists. This is how SPM achieves
at-least-once delivery:

```go
if req.Params.Id != nil && *req.Params.Id != "" {
    if existing, err := s.store.Get(*req.Params.Id); err == nil {
        return CreateVM201JSONResponse(entryToVM(*existing)), nil
    }
}
```

### Registration is upsert

SPM's `CreateProvider` returns 200 (updated) or 201 (created). Design your
registrar to handle both. Use stable provider IDs across restarts
(`PROVIDER_ID_VM` env var) so you do not orphan registrations.

## 6. `provider_hints` is the escape hatch — document it well

The DCM catalog specs (`VMSpec`, `ClusterSpec`) are intentionally
provider-agnostic. Provider-specific config goes in `provider_hints`, nested
under the provider key:

```yaml
provider_hints:
  kcli:
    profile: "rhel-9"
    cluster_type: "k3s"
```

We hit a real bug during E2E testing because the catalog flow sent
`provider_hints.cluster_type` instead of `provider_hints.kcli.cluster_type`. The
nesting under the provider key (`kcli`) is the convention. Document the expected
structure explicitly in your enhancement proposal and your OpenAPI spec
description.

## 7. Wrap your backend client defensively

The kweb client taught us several patterns that apply to any backend integration.

### Rate limiter

Even with a local backend, thundering herd from monitor polls plus API requests
can overwhelm it:

```go
limiter: rate.NewLimiter(rate.Limit(10), 20),
```

### Sentinel errors

Define `ErrKwebUnreachable`, `ErrConflict`, `ErrNotFound` so handlers can match
with `errors.Is()` and return appropriate HTTP status codes (502, 409, 404)
without coupling to the backend's error format.

### Parse HTML error pages

Many backends return HTML on errors instead of JSON. Your client must detect this
and wrap it into a structured error:

```go
if strings.Contains(raw, "<html") || strings.Contains(raw, "<!DOCTYPE") {
    kErr.Reason = fmt.Sprintf("kweb returned HTML error (HTTP %d)", statusCode)
}
```

### Parse "success" failures

kweb returns `200 OK` with `{"result": "failure", "reason": "already exists"}`.
Your client must inspect the response body even on 2xx status codes.

## 8. The monitor pattern: poll + debounce + orphan detection

Do not rely on the backend to push status changes. Poll it.

- **Poll interval** (30s default): List all resources from the backend, compare
  status with the store, publish events for changes.
- **Debounce window** (5s): Prevent event storms when status flaps rapidly.
- **Orphan detection**: Any `dcm-*` resource in the backend that is not in your
  store is an orphan. Log a warning. Do not auto-delete — that is a dangerous
  assumption.
- **Timeout for async operations**: Clusters take minutes to create. Track
  `createdAt` and timeout after a configurable `ClusterCreateTimeout` (30m
  default).

## 9. RFC 7807 everywhere, no exceptions

Every error response must be `application/problem+json`. SPM expects it. The DCM
UI parses it.

Use a shared `ProblemDetail` helper:

```go
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
    pd := ProblemDetail{
        Type:   "about:blank",
        Title:  http.StatusText(status),
        Status: status,
        Detail: detail,
    }
    w.Header().Set("Content-Type", "application/problem+json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(pd)
}
```

Also add a `PanicRecovery` middleware that returns 500 as RFC 7807 instead of the
default Go stack trace. Panics in production should not leak implementation
details.

## 10. Self-probe before registering with SPM

Start the HTTP server, then hit your own `/health` endpoint before calling SPM.
If your server cannot serve health checks, registration is pointless and SPM will
just mark you as `not_ready`:

```go
go func() { _ = s.httpServer.Serve(s.listener) }()

if !s.selfProbe(ctx) {
    return fmt.Errorf("self-probe failed")
}

// Now safe to register with SPM
for _, reg := range s.registrars {
    reg.StartBackground(ctx)
}
```

## 11. The linting tax: pay it upfront

This is where we lost the most time. The DCM project uses a comprehensive
`golangci-lint` config with 30+ linters. The "fix it later" approach cost us 186
violations across 15 files in a single cleanup session.

### Linters that bite hardest

| Linter | What it catches | Fix |
|--------|----------------|-----|
| `errcheck` | Unchecked return values | Use `_ =` for intentional discards |
| `revive/unused-parameter` | Unused function params | Rename to `_` (common in interface impls and test mocks) |
| `errchkjson` | `json.Encode` on types with `interface{}` fields | Add `//nolint:errchkjson` with a comment explaining safety |
| `nilerr` | Returning `nil` error after checking `err != nil` elsewhere | Add `//nolint:nilerr` when translating parse errors into HTTP 400s |
| `staticcheck/ST1000` | Missing package documentation comment | Add a doc comment to the first file in every package |
| `gocritic/ifElseChain` | Long if-else if-else chains | Rewrite as `switch` |
| `gofumpt` | Formatting stricter than `gofmt` | Run on save from day one |
| `usestdlibvars` | Magic numbers for HTTP status codes | Use `http.StatusOK` etc. |
| `ginkgolinter` | Wrong Gomega assertion style | Use `BeEmpty()` not `HaveLen(0)` |

### Do this on day one

1. Copy `.golangci.yml` from an existing DCM SP (they are all identical).
2. Add an `errcheck` exclusion for `_test.go` files to reduce noise.
3. Run `golangci-lint run ./...` after every file you write, not after you are
   "done".
4. Configure your editor to run `gofumpt` on save.
5. Use `golangci-lint run --fix ./...` periodically to auto-fix what it can.

## 12. Configuration: env vars with sane defaults

Every config value should come from an env var with a sensible default. Group
timeouts together. Fail fast on missing required vars (`KWEB_URL`, `SPM_URL`).

Use `time.ParseDuration` for duration strings so operators can write `30s`
instead of `30000`:

```go
func parseDuration(envKey, fallback string) (time.Duration, error) {
    raw := envOrDefault(envKey, fallback)
    d, err := time.ParseDuration(raw)
    if err != nil {
        return 0, fmt.Errorf("invalid duration for %s: %q: %w", envKey, raw, err)
    }
    return d, nil
}
```

**Watch out for defaults that assume containers.** The default
`STATE_STORE_PATH=/data/state.db` broke during bare-metal E2E testing because
`/data/` did not exist. Always allow overriding paths.

## 13. Graceful shutdown matters

Your SP holds a bbolt file lock, an HTTP server, a NATS connection, and a monitor
goroutine. Shutdown order matters:

1. Stop accepting new HTTP requests (`httpServer.Shutdown`)
2. Cancel the monitor context
3. Wait for in-flight work (with a timeout)
4. Close NATS publisher
5. Close bbolt store

Getting this wrong leads to corrupted state files or orphaned resources in the
backend.

---

## Quick-start checklist for a new SP

- [ ] Write the OpenAPI spec (`openapi.yaml`)
- [ ] Generate types and strict server with `oapi-codegen`
- [ ] Copy `.golangci.yml` from an existing SP
- [ ] Implement the backend client behind an interface with sentinel errors
- [ ] Implement the state store (bbolt or equivalent)
- [ ] Implement health endpoints (one per service type + one global)
- [ ] Implement CRUD handlers with idempotent create (`?id=`)
- [ ] Implement the monitor (poll + debounce + orphan detection)
- [ ] Implement the registrar (upsert with exponential backoff)
- [ ] Add self-probe before registration
- [ ] Add `PanicRecovery` middleware returning RFC 7807
- [ ] Add `provider_hints` documentation to your enhancement proposal
- [ ] Run `golangci-lint run ./...` — fix everything before your first commit
- [ ] E2E test against a live DCM stack (podman-compose) before opening the PR
