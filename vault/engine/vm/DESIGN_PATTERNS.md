# VM Package — Design Patterns

## Strategy Pattern

The `vm` package uses the **Strategy pattern** to decouple VM lifecycle management from any specific hypervisor implementation.

### How it works

An interface (`VM`) defines the contract that all hypervisor backends must satisfy:

```go
type VM interface {
    Start(ctx context.Context) error
    Stop() error
}
```

Each hypervisor is a **concrete strategy** that implements this interface. Currently we have one:

- `QEMU` — launches a QEMU process with architecture-aware defaults

Callers program against the `VM` interface, never the concrete type:

```go
var vm engine.VM = engine.NewQEMU(cfg)
vm.Start(ctx)
vm.Stop()
```

### Why Strategy over other patterns

| Pattern     | Why not                                                                                                                                       |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| **Factory** | We don't need runtime selection from a string/enum — the caller explicitly picks a backend. A factory can be added later if needed.           |
| **Adapter** | Adapter wraps an incompatible interface to make it compatible. Here we're designing the interface from scratch, not adapting an existing one. |
| **Facade**  | Facade simplifies a complex subsystem. Our interface isn't hiding complexity — it's enabling substitution.                                    |

### Adding a new backend

1. Create a new file (e.g. `firecracker.go`) in this package.
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
    cfg     FirecrackerConfig
    // ...
}

func NewFirecracker(cfg FirecrackerConfig) *Firecracker { ... }
func (f *Firecracker) Start(ctx context.Context) error  { ... }
func (f *Firecracker) Stop() error                      { ... }
```

### Config composition

Backend-specific config structs **embed** the shared `Config`:

```
Config (shared: kernel, initrd, vcpus, memory)
  └── QEMUConfig (adds: Accel)
  └── FirecrackerConfig (adds: SocketPath)
```

This keeps universal fields in one place while letting each backend carry its own options.
