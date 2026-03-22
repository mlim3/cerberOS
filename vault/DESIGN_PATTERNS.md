# cerberOS Vault — Design Patterns

## VM Package — Strategy Pattern

The `vm` package uses the **Strategy pattern** to decouple VM lifecycle management from any specific hypervisor implementation.

### How it works

An interface (`VM`) defines the contract that all hypervisor backends must satisfy:

```go
type VM interface {
    Start(ctx context.Context) error
    Stop() error
    Run(ctx context.Context) (*RunResult, error)
}
```

Each hypervisor is a **concrete strategy** that implements this interface. Currently we have one:

- `QEMU` — launches a QEMU process with architecture-aware defaults

Callers program against the `VM` interface, never the concrete type:

```go
var vm engine.VM = engine.NewQEMU(cfg)
vm.Start(ctx)   // long-lived VM (manual control)
vm.Run(ctx)     // ephemeral VM (boot → run → exit, output captured)
vm.Stop()
```

### Why Strategy over other patterns

| Pattern     | Why not                                                                                                                                       |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| **Factory** | We don't need runtime selection from a string/enum — the caller explicitly picks a backend. A factory can be added later if needed.           |
| **Adapter** | Adapter wraps an incompatible interface to make it compatible. Here we're designing the interface from scratch, not adapting an existing one. |
| **Facade**  | Facade simplifies a complex subsystem. Our interface isn't hiding complexity — it's enabling substitution.                                    |

### Adding a new backend

1. Create a new file (e.g. `firecracker.go`) in `engine/vm/`.
2. Define a config struct embedding `Config` for any backend-specific options.
3. Implement the `VM` interface on your struct.
4. Update `main.go` to instantiate the new backend.

Example skeleton:

```go
type FirecrackerConfig struct {
    Config
    SocketPath string
}

type Firecracker struct {
    cfg FirecrackerConfig
    // ...
}

func NewFirecracker(cfg FirecrackerConfig) *Firecracker { ... }
func (f *Firecracker) Start(ctx context.Context) error  { ... }
func (f *Firecracker) Stop() error                      { ... }
func (f *Firecracker) Run(ctx context.Context) (*RunResult, error) { ... }
```

### Config composition

Backend-specific config structs **embed** the shared `Config`:

```
Config (shared: kernel, initrd, vcpus, memory)
  └── QEMUConfig (adds: Accel)
  └── FirecrackerConfig (adds: SocketPath)
```

This keeps universal fields in one place while letting each backend carry its own options.

---

## Preprocessor Package — Strategy + Template Method

The `preprocessor` package uses two patterns together:

**Strategy** for secret resolution — the `SecretStore` interface abstracts where secrets come from, so the preprocessor logic never changes when the backend does:

```go
type SecretStore interface {
    Get(key string) (string, error)
}
```

**Template Method** for the processing pipeline — `Process()` is a fixed sequence of steps (validate → substitute → validate), with hooks left open for future validators without changing the skeleton:

```go
// Current pipeline (template):
func (p *Preprocessor) Process(raw []byte) (*Result, error) {
    // [hook] pre-substitution validators (e.g. syntax check, disallowed commands)
    // substitute {{PLACEHOLDER}} → secret values
    // [hook] post-substitution validators (e.g. no unresolved placeholders, size limits)
    // return Result{Script, InjectedSecrets}
}
```

### Why this combination

The two patterns solve different problems:
- **Strategy** makes the secret backend swappable (MockStore → HashiCorp Vault → KMS) without touching `Process()`.
- **Template Method** makes the pipeline extensible (add validators) without changing callers.

### Adding a real secret store

1. Create a struct implementing `SecretStore`.
2. Pass it to `preprocessor.New()`.

```go
type VaultStore struct { client *vault.Client }

func (v *VaultStore) Get(key string) (string, error) {
    // fetch from HashiCorp Vault, KMS, etc.
}

pp := preprocessor.New(&VaultStore{client: vaultClient})
```

### Adding validators (Template Method extension)

Add a `[]Validator` field to `Preprocessor` and iterate in `Process()`:

```go
type Validator interface {
    Validate(script []byte) error
}

type Preprocessor struct {
    store      SecretStore
    validators []Validator  // pre- or post-substitution
}
```

Pre-substitution validators see the raw script (good for syntax checks, disallowed command patterns).
Post-substitution validators see the resolved script (good for size limits, no unresolved `{{...}}` markers remaining).

---

## Orchestrator Package — Pipeline / Chain of Responsibility

The `orchestrator` package uses the **Pipeline pattern** (a linear chain of responsibility) to coordinate the full execution flow. Each step transforms or consumes the output of the previous one:

```
Request
  │
  ▼
[1] Preprocessor.Process()   — inject secrets, collect injected values
  │
  ▼
[2] initrd.Builder.Build()   — embed processed script into ephemeral initrd
  │
  ▼
[3] vm.NewQEMU(cfg).Run()    — boot VM, execute script, capture output
  │
  ▼
[4] extractJobOutput()       — strip kernel boot noise via sentinel markers
  │
  ▼
[5] scrubSecrets()           — replace injected secret values with [REDACTED]
  │
  ▼
Response{Output, ExitCode}
```

### Why Pipeline over other patterns

| Pattern                  | Why not                                                                                                              |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------- |
| **Facade**               | A facade hides complexity but doesn't enforce ordering. The pipeline's sequential dependency is the point.           |
| **Mediator**             | Mediator manages many-to-many coordination. This is strictly linear — each step feeds exactly one next step.         |
| **Builder (GoF)**        | Builder constructs objects. The orchestrator constructs an execution result, but the flow is imperative, not fluent. |

### Security invariant

The pipeline enforces the core security property of vault: **agents never touch secrets**.

- The agent only submits a script with `{{PLACEHOLDER}}` markers — it never sees secret values.
- Step 1 resolves placeholders inside vault, unreachable by the agent.
- Step 5 scrubs any secrets that leaked into the output (e.g. `echo $API_KEY`) before returning to the caller.

### Extending the pipeline

To add a new step (e.g. output size limiting, audit logging), add it between the relevant stages in `Execute()`. Each step is a pure function — no shared state between steps.

```go
// Example: add audit logging after execution
runResult, err := machine.Run(ctx)
auditLog(req, runResult)          // new step
output := extractJobOutput(...)
```

Steps that need access to injected secret values (like scrubbing) must run after step 1 returns `Result.InjectedSecrets`.

---

## Audit Package — Observer (with pluggable exporters)

The `engine/audit` package (and the mirror implementation inside `secretstore`) uses the **Observer pattern** to decouple *what* is logged from *where* it goes.

### How it works

`Logger` is the subject. It holds a list of `Exporter` observers and fans each `Event` out to all of them:

```go
type Exporter interface {
    Export(e Event) error
}

type Logger struct {
    exporters []Exporter
}

func (l *Logger) Log(e Event) { /* fan-out to all exporters */ }
```

Built-in exporters ship with the package:

| Exporter | Behaviour |
|---|---|
| `JSONExporter` | Writes newline-delimited JSON to any `io.Writer` (stdout, file, …) |
| `MultiExporter` | Composes multiple exporters into one — useful for fan-out without changing callers |

### Event kinds

| Kind | Emitted by | What it records |
|---|---|---|
| `execution` | `orchestrator.Execute` | Agent identifier + "submitted script" |
| `secret_access` | `preprocessor.Process` | Agent identifier + secret *names* (never values) |
| `secret_access` | `secretstore.handleResolve` | Resolved key names + caller source IP |

The secretstore is a separate module and mirrors the pattern locally (`auditLogger` / `auditExporter`) rather than importing `engine/audit`.

### Why Observer over other patterns

| Pattern | Why not |
|---|---|
| **Decorator** | Decorator wraps a single object to add behaviour. Here we're broadcasting to *N* independent consumers — fan-out, not wrapping. |
| **Chain of Responsibility** | CoR passes a request down a chain until one handler claims it. We want *all* exporters to receive every event, not just the first match. |
| **Mediator** | Mediator coordinates bidirectional many-to-many communication. Log emission is strictly one-way (Logger → Exporters). |

### Adding a new exporter

Implement `audit.Exporter` and pass it to `audit.New`:

```go
type WebhookExporter struct{ endpoint string }

func (w *WebhookExporter) Export(e audit.Event) error {
    b, _ := json.Marshal(e)
    _, err := http.Post(w.endpoint, "application/json", bytes.NewReader(b))
    return err
}

auditor := audit.New(
    audit.NewJSONExporter(os.Stdout),
    &WebhookExporter{endpoint: "https://ingest.example.com/audit"},
)
```

No other code changes required — `Logger`, `Preprocessor`, and `Orchestrator` are unaware of the new destination.
