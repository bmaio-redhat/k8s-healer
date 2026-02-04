# k8s-healer Code Style and Structure Guidelines

This document describes code style, structure, and conventions for the k8s-healer project. Follow it when writing or modifying code to keep the codebase consistent and maintainable.

---

## 1. Project Layout

```
k8s-healer/
├── cmd/
│   └── main.go              # CLI entrypoint, flags, cobra commands
├── pkg/
│   ├── daemon/              # Daemonization (PID, log redirection)
│   ├── discovery/           # Cluster endpoint discovery
│   ├── healer/              # Core healing, CRD cleanup, namespaces, memory
│   ├── healthcheck/         # Cluster health checks and formatting
│   └── util/                # Shared checks (pods, nodes, VMs, CRDs, strain)
├── go.mod
├── go.sum
├── Makefile
├── Dockerfile
├── CONTRIBUTING.md          # Design philosophy and evolution
├── STYLE.md                 # This file
└── README.md
```

- **`cmd/`**: Single entrypoint; no business logic. Wire flags, build healer, run commands.
- **`pkg/`**: All library code. Each package is a single, clear responsibility.
- **Root**: Build, run, and docs only (scripts, Dockerfile, Makefile, markdown).

---

## 2. Naming Conventions

### 2.1 Files

- **Implementation**: `lowercase.go` or `descriptive_name.go` (e.g. `healer.go`, `daemon.go`, `checks.go`).
- **Tests**: `*_test.go`. Prefer one test file per logical area when a package grows large:
  - `healer_test.go` – helpers and core tests
  - `healer_namespaces_test.go` – namespace discovery and cleanup
  - `healer_memory_test.go` – memory limit and sanitization
  - `checks_test.go`, `checks_vm_crd_test.go`, `checks_strain_eviction_test.go`

### 2.2 Packages

- Short, lowercase, single-word where possible: `healer`, `daemon`, `util`, `healthcheck`, `discovery`.
- Package name should match the directory and reflect responsibility.

### 2.3 Identifiers

- **Exported types/functions**: `PascalCase` (e.g. `Healer`, `NewHealer`, `PerformClusterHealthCheck`).
- **Unexported**: `camelCase` (e.g. `createTestHealer`, `checkMemory`, `doCRDCleanup`).
- **Constants**: `PascalCase` for exported, `camelCase` for package-private (e.g. `DefaultPIDFile`, `DefaultRestartThreshold`).
- **Interfaces**: Prefer `-er` when it’s a single-method role (e.g. `Reader`). For multi-method interfaces, a noun is fine (e.g. `kubernetes.Interface`).
- **Mutexes**: Suffix with `Mu` and keep unexported (e.g. `healedPodsMu`, `trackedCRDsMu`).

### 2.4 CLI and flags

- Flags: `kebab-case` in usage (e.g. `--enable-vm-healing`, `--stale-age`).
- Go variables for flags: `camelCase` (e.g. `enableVMHealing`, `staleAge`).

---

## 3. Code Structure

### 3.1 File order (within a .go file)

1. Package clause and imports (grouped: stdlib, blank line, external, blank line, project imports).
2. Package-level constants and vars.
3. Exported types and constructors.
4. Exported functions/methods.
5. Unexported types, then unexported functions/methods.

Keep related types and their methods close; long files can be split by theme (e.g. “namespace”, “memory”, “CRD cleanup”) into multiple `_test.go` files, not necessarily multiple implementation files unless the package is very large.

### 3.2 Structs

- Put related fields together; group with blank lines if helpful.
- Document exported fields and non-obvious unexported ones.
- Align field comments only when it improves readability; don’t force alignment everywhere.

```go
// Healer holds the Kubernetes client and configuration for watching.
type Healer struct {
	ClientSet     kubernetes.Interface
	DynamicClient dynamic.Interface
	Namespaces    []string
	namespacesMu  sync.RWMutex // Protects Namespaces slice
	StopCh        chan struct{}
	// ...
}
```

### 3.3 Constructors and dependency injection

- Use `New*` for constructors that return `(*T, error)` (e.g. `NewHealer`).
- Accept dependencies explicitly (clients, config) so tests can inject fakes.
- Optional test hooks can be fields (e.g. `MemoryReadFunc func() uint64` in Healer).

---

## 4. Error Handling

- Use `fmt.Errorf("...: %w", err)` so callers can use `errors.Is` / `errors.As`.
- Return errors from the layer that can add context; avoid wrapping the same error many times.
- For user-facing or log-worthy messages, use clear, actionable text (e.g. “failed to build Kubernetes config”, “failed to write PID file”).
- Use `apierrors.IsNotFound(err)` (and similar) for API errors instead of string matching when possible.

---

## 5. Concurrency and shared state

- Protect shared maps/slices with `sync.RWMutex`; name the mutex after what it protects (e.g. `healedPodsMu` for `HealedPods`).
- Prefer `RLock`/`RUnlock` for read-only paths; use `Lock`/`Unlock` only when mutating.
- For one-shot “done” or “restart requested” signals, use `chan struct{}` (closed when signalled) or an `atomic`/`sync.Once` pattern so main can react (e.g. exit) without races.
- Start goroutines from a single place where possible (e.g. `Watch()`); coordinate shutdown via `StopCh` or context.

---

## 6. Comments and documentation

- Every exported type, function, and method should have a doc comment starting with the name (e.g. `// NewHealer initializes ...`).
- Prefer full sentences and explain “why” for non-obvious behavior (e.g. rate limits, timeouts, retries).
- Inline comments for complex logic or important invariants (e.g. “Remove 30%”, “0 = disabled”).
- Keep comments up to date when behavior changes.

---

## 7. Testing

- **Location**: Tests live in `*_test.go` in the same package (package `healer`, `util`, etc.) for white-box testing.
- **Helpers**: Shared test helpers (e.g. `createTestHealer`, `createTestHealerWithExclusions`) live in the main `*_test.go` or a dedicated test file in the same package.
- **Naming**: `Test<Function>` or `Test<Receiver>_<Method>_<Scenario>` (e.g. `TestIsRateLimitError`, `TestHealer_DoCRDCleanup_ResourceNotFound`).
- **Tables**: Use table-driven tests for multiple cases; keep cases readable and names descriptive.
- **Fakes**: Use `k8s.io/client-go/kubernetes/fake` and `k8s.io/client-go/dynamic/fake` for API tests; register list kinds where needed (e.g. `NewSimpleDynamicClientWithCustomListKinds`).
- **Coverage**: Run `go test -cover ./pkg/...`; add tests for new behavior and important error paths.

---

## 8. Imports and formatting

- Run `goimports` or `gofmt -s`; keep import blocks grouped (stdlib, third-party, project).
- Use short, meaningful import aliases when necessary (e.g. `v1 "k8s.io/api/core/v1"`, `apierrors "k8s.io/apimachinery/pkg/api/errors"`).
- Prefer the standard library (e.g. `strings`, `context`, `time`) over custom helpers when equivalent.

---

## 9. Logging and observability

- Use `fmt.Printf` for user-facing or operational messages (as in the current codebase).
- Use a consistent prefix for categories, e.g. `[Check]`, `[INFO]`, `[WARN]`, `[FAIL]`, `[SKIP]`, `[SUCCESS]`.
- Include resource identifiers (namespace/name, GVR) in messages so logs are actionable.
- For daemon mode, ensure important events are written to the configured log file (stdout/stderr redirected).

---

## 10. Configuration and defaults

- Defaults: Define in one place (e.g. `NewHealer`, or `cmd/main.go` for CLI defaults).
- Use meaningful default values and document them (e.g. cooldown, stale age, memory limit, intervals).
- CLI: Use `cobra` for commands and flags; keep flag names and help text clear and consistent with existing flags.

---

## 11. Dependencies

- Prefer the standard library and a small set of stable deps (`k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery`, `github.com/spf13/cobra`).
- Pin versions in `go.mod`; run `go mod tidy` and avoid unnecessary new dependencies.

---

## 12. Summary checklist

When adding or changing code:

- [ ] Follow the project layout and put code in the right package.
- [ ] Use the naming conventions above (files, packages, exported/unexported).
- [ ] Document exported symbols and non-obvious behavior.
- [ ] Use structured errors with `%w` and API helpers like `apierrors.IsNotFound`.
- [ ] Protect shared state with mutexes; use channels or atomics for signals.
- [ ] Add or extend tests (table-driven where appropriate) and run `go test ./pkg/...`.
- [ ] Keep logging consistent (prefixes, resource identifiers).
- [ ] Keep configuration and defaults in one place and document them.

For design philosophy, feature evolution, and contribution workflow, see [CONTRIBUTING.md](CONTRIBUTING.md).
