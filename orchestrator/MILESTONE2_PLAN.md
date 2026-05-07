# Milestone 2 Implementation Plan — Centralized Logging & Distributed Tracing

**Branch:** `orchestrator-milestone-2`
**Component:** Orchestrator
**Goal:** Add structured logging with trace ID propagation and stand up a Grafana + Loki + Tempo observability stack so any task can be traced end-to-end across components.
**Reference:** EDD v3.1 §15 (Observability Design)

---

## Overview

This milestone adds two debugging capabilities:

1. **Centralized structured logging** — every component writes JSON logs to stdout, Promtail scrapes Docker container stdout, Loki stores them, Grafana displays them. Every log line carries a `trace_id` so you can grep one request across all components.

2. **Distributed tracing** — every task gets a unique `trace_id` at the entry point that propagates through all internal modules and all outbound NATS messages. OpenTelemetry produces span trees exported to Grafana Tempo, so the full lifecycle of a task is visualized as a waterfall diagram.

**Stack:**
- **Loki** — log aggregation
- **Promtail** — Docker stdout scraper
- **Grafana Tempo** — distributed tracing backend (OTLP-compatible)
- **Grafana** — unified UI for logs and traces
- **OpenTelemetry Go SDK** — instrumentation
- **log/slog** — structured logging (Go stdlib)

All services run in the existing `docker-compose.yml`.

---

## Implementation Steps

Work through these steps in order. Each step is independently verifiable. Commit after each step with a clear message so the PR history shows the progression.

---

### Step 1 — Add `trace_id` field to the message envelope

**Files to touch:**
- `pkg/types/envelope.go` (or wherever `MessageEnvelope` is defined)
- Any test files using the envelope

**Changes:**

Add `TraceID` and `SpanID` fields to the envelope struct:

```go
type MessageEnvelope struct {
    MessageID       string          `json:"message_id"`
    MessageType     string          `json:"message_type"`
    SourceComponent string          `json:"source_component"`
    CorrelationID   string          `json:"correlation_id"`
    TraceID         string          `json:"trace_id"`            // NEW
    SpanID          string          `json:"span_id,omitempty"`   // NEW
    Timestamp       string          `json:"timestamp"`
    SchemaVersion   string          `json:"schema_version"`
    Payload         json.RawMessage `json:"payload"`
}
```

**Backward compatibility:** Envelope validation MUST accept messages with an empty `trace_id`. If it's empty on an inbound message, the Communications Gateway will generate one in Step 3.

**Verification:**
- Existing tests still pass.
- Marshaling and unmarshaling the envelope includes the new fields.

---

### Step 2 — Create the observability package

**Files to create:**
- `internal/observability/logger.go`
- `internal/observability/context.go`
- `internal/observability/logger_test.go`

**What to build:**

#### 2a. Context helpers (`context.go`)

Define context keys and helpers to carry correlation IDs through the call chain:

```go
package observability

import "context"

type ctxKey int

const (
    traceIDKey ctxKey = iota
    taskIDKey
    planIDKey
    subtaskIDKey
    moduleKey
)

func WithTraceID(ctx context.Context, id string) context.Context  { ... }
func WithTaskID(ctx context.Context, id string) context.Context   { ... }
func WithPlanID(ctx context.Context, id string) context.Context   { ... }
func WithSubtaskID(ctx context.Context, id string) context.Context{ ... }
func WithModule(ctx context.Context, name string) context.Context { ... }

func TraceIDFrom(ctx context.Context) string    { ... }
func TaskIDFrom(ctx context.Context) string     { ... }
func PlanIDFrom(ctx context.Context) string     { ... }
func SubtaskIDFrom(ctx context.Context) string  { ... }
func ModuleFrom(ctx context.Context) string     { ... }
```

#### 2b. Logger (`logger.go`)

Use `log/slog` from the Go stdlib. No third-party logging libraries.

```go
package observability

import (
    "context"
    "log/slog"
    "os"
)

var defaultLogger *slog.Logger

// InitLogger sets up the global logger. Called once from main.go.
// level: "debug" | "info" | "warn" | "error"
// format: "json" (production) | "text" (local dev)
func InitLogger(level string, format string, nodeID string) { ... }

// LogFromContext returns a logger with all IDs from the context pre-attached.
// Use this EVERYWHERE instead of log.Printf or slog.Default().
func LogFromContext(ctx context.Context) *slog.Logger { ... }
```

**Behavior of `LogFromContext`:**
- Always includes `component=orchestrator` and `node_id`.
- Pulls `trace_id`, `task_id`, `plan_id`, `subtask_id`, `module` from context.
- Returns a `*slog.Logger` with those as pre-attached attributes.

**Required log fields on every line** (per EDD §15.1):
- `timestamp` (slog handles automatically)
- `level` (slog handles automatically)
- `component` = `"orchestrator"`
- `module` (from context if set)
- `node_id`
- `trace_id` (from context if set)
- `task_id` (from context if set)
- `plan_id` (from context if set)
- `subtask_id` (from context if set)
- `message` (slog handles automatically)

**Forbidden log content** (per EDD §15.1):
- Raw user input (`payload.raw_input`)
- Credential values
- Task result payloads
- Planner output

#### 2c. Tests (`logger_test.go`)

Write tests that verify:
- `LogFromContext` includes trace_id when set in context.
- `LogFromContext` works when context has no IDs (returns valid logger).
- JSON output contains all required fields.
- Nested context (trace_id set, then task_id added) includes both.

**Verification:**
- `go test ./internal/observability/...` passes.

---

### Step 3 — Generate trace_id at the entry point

**Files to touch:**
- `internal/comms/gateway.go` (Communications Gateway — M1)

**Changes:**

In the `user_task` inbound handler:

1. After envelope validation, check for `trace_id`:
   ```go
   traceID := env.TraceID
   if traceID == "" {
       traceID = uuid.New().String()
   }
   ```

2. Create the root context:
   ```go
   ctx := context.Background()
   ctx = observability.WithTraceID(ctx, traceID)
   ctx = observability.WithTaskID(ctx, task.TaskID)
   ctx = observability.WithModule(ctx, "comms_gateway")
   ```

3. Log the task receipt:
   ```go
   observability.LogFromContext(ctx).Info("user task received",
       "user_id", task.UserID,
       "priority", task.Priority)
   ```

4. Pass `ctx` to the Task Dispatcher:
   ```go
   g.dispatcher.HandleInboundTask(ctx, task)
   ```

**Verification:**
- Submit a test task to the orchestrator.
- The stdout log line for "user task received" contains a trace_id.

---

### Step 4 — Propagate context through all modules

**Files to touch:**
- `internal/dispatcher/dispatcher.go` (Task Dispatcher — M2)
- `internal/policy/enforcer.go` (Policy Enforcer — M3)
- `internal/monitor/monitor.go` (Task Monitor — M4)
- `internal/recovery/manager.go` (Recovery Manager — M5)
- `internal/memory/interface.go` (Memory Interface — M6)
- `internal/executor/plan_executor.go` (Plan Executor — M7)

**Changes:**

1. Add `ctx context.Context` as the **first parameter** of every function that operates on a task, plan, or subtask. Examples:
   ```go
   func (d *TaskDispatcher) HandleInboundTask(ctx context.Context, task UserTask) error
   func (d *TaskDispatcher) HandleDecompositionResponse(ctx context.Context, resp DecompositionResponse) error
   func (p *PolicyEnforcer) Validate(ctx context.Context, req PolicyCheckRequest) (PolicyResult, error)
   func (e *PlanExecutor) Execute(ctx context.Context, plan ExecutionPlan, ts *TaskState) error
   func (e *PlanExecutor) dispatchReadySubtasks(ctx context.Context, plan ExecutionPlan, ts *TaskState) error
   func (m *MemoryInterface) Write(ctx context.Context, payload MemoryWritePayload) error
   func (m *MemoryInterface) Read(ctx context.Context, query MemoryQuery) ([]Record, error)
   ```

2. When entering a module, update the context with the module name:
   ```go
   func (d *TaskDispatcher) HandleInboundTask(ctx context.Context, task UserTask) error {
       ctx = observability.WithModule(ctx, "task_dispatcher")
       log := observability.LogFromContext(ctx)
       log.Info("task dispatch pipeline started")
       // ...
   }
   ```

3. When the Plan Executor dispatches a subtask, derive a child context:
   ```go
   subCtx := observability.WithPlanID(ctx, plan.PlanID)
   subCtx = observability.WithSubtaskID(subCtx, sub.SubtaskID)
   subCtx = observability.WithModule(subCtx, "plan_executor")
   log := observability.LogFromContext(subCtx)
   log.Info("dispatching subtask", "depends_on", sub.DependsOn)
   ```

4. **Replace every existing `log.Printf`, `fmt.Println`, `slog.Default()`, or bare logger call** with `observability.LogFromContext(ctx).Info(...)` (or `Warn`, `Error`, `Debug`).

**Verification:**
- Build succeeds: `go build ./...`
- Submit a test task and grep stdout for the trace_id:
  ```bash
  docker compose logs orchestrator | grep "trace_id=<the-uuid>"
  ```
- You should see the full sequence of log lines for one request from receipt to dispatch.

---

### Step 5 — Stamp trace_id on outbound NATS messages

**Files to touch:**
- `internal/comms/gateway.go` (publish helpers)

**Changes:**

1. Update the outbound publish helper signature to take a context:
   ```go
   func (g *Gateway) Publish(ctx context.Context, subject string, payload interface{}) error {
       env := MessageEnvelope{
           MessageID:       uuid.New().String(),
           MessageType:     inferMessageType(subject),
           SourceComponent: "orchestrator",
           CorrelationID:   observability.TaskIDFrom(ctx),
           TraceID:         observability.TraceIDFrom(ctx),  // NEW
           Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
           SchemaVersion:   "1.0",
       }
       envBytes, err := json.Marshal(...)
       // ...
       return g.nc.Publish(subject, envBytes)
   }
   ```

2. Update ALL outbound publish call sites to pass `ctx`:
   - Planner `task.inbound` publish (Task Dispatcher → Comms → NATS)
   - Subtask `task.inbound` publish (Plan Executor → Comms → NATS)
   - `capability.query` publish (Plan Executor → Comms → NATS)
   - `agent_terminate` publish (Recovery Manager → Comms → NATS)
   - `task.result` / `task.failed` back to User I/O (Task Dispatcher → Comms → NATS)
   - `task_accepted` / `task_failed` / `policy_violation` responses
   - IO Component HTTP push events (from §11.6 — carry trace_id in the event body too)

3. For the IO Component HTTP client (`internal/io/client.go`), add `trace_id` to the event payload:
   ```go
   type StatusEvent struct {
       Type    string      `json:"type"`
       TraceID string      `json:"trace_id"`  // NEW
       Payload interface{} `json:"payload"`
   }
   ```

**Verification:**
- Subscribe to `aegis.agents.task.inbound` with a NATS CLI:
  ```bash
  nats sub "aegis.agents.task.inbound"
  ```
- Submit a test task.
- Verify the published envelope JSON contains a non-empty `trace_id` field matching the one you logged in Step 3.

---

### Step 6 — Extract trace_id on inbound NATS messages

**Files to touch:**
- `internal/comms/gateway.go` (inbound subscribers)

**Changes:**

For every inbound NATS subscriber in the Communications Gateway, extract the trace_id from the envelope and hydrate a fresh context before calling the handler:

```go
func (g *Gateway) handleTaskResult(msg *nats.Msg) {
    var env MessageEnvelope
    if err := json.Unmarshal(msg.Data, &env); err != nil {
        // handle error
        return
    }

    ctx := context.Background()
    if env.TraceID != "" {
        ctx = observability.WithTraceID(ctx, env.TraceID)
    }
    // Extract task_id / plan_id / subtask_id from the payload if present
    if taskID := extractTaskIDFromPayload(env.Payload); taskID != "" {
        ctx = observability.WithTaskID(ctx, taskID)
    }
    ctx = observability.WithModule(ctx, "comms_gateway")

    observability.LogFromContext(ctx).Info("received task result from agents")

    // Dispatch to handler
    g.dispatcher.HandleTaskResult(ctx, env.Payload)
}
```

**Apply to all inbound subjects:**
- `aegis.orchestrator.tasks.inbound` (User I/O → orchestrator) — Step 3 already handles this
- `aegis.orchestrator.task.accepted`
- `aegis.orchestrator.task.result`
- `aegis.orchestrator.task.failed`
- `aegis.orchestrator.agent.status`
- `aegis.orchestrator.capability.response`
- `aegis.orchestrator.credential.request`

**Verification:**
- Send a test task through the full pipeline.
- Verify that when the planner task returns a result, the orchestrator's log line for "received task result from agents" has the **same trace_id** as the original "user task received" log line.

---

### Step 7 — Add OpenTelemetry tracing for span trees

**Files to create:**
- `internal/observability/tracing.go`

**Dependencies to add (`go.mod`):**
```
go.opentelemetry.io/otel
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
go.opentelemetry.io/otel/sdk/resource
go.opentelemetry.io/otel/sdk/trace
go.opentelemetry.io/otel/semconv/v1.24.0
go.opentelemetry.io/otel/trace
```

**What to build:**

```go
package observability

import (
    "context"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/sdk/resource"
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
    "go.opentelemetry.io/otel/trace"
)

// InitTracer sets up the OTLP exporter. Called once from main.go.
// Returns a shutdown function to be called on graceful exit.
func InitTracer(ctx context.Context, endpoint string, nodeID string) (func(context.Context) error, error) {
    exporter, err := otlptracegrpc.New(ctx,
        otlptracegrpc.WithEndpoint(endpoint),
        otlptracegrpc.WithInsecure(),
    )
    if err != nil {
        return nil, err
    }

    res, err := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceName("orchestrator"),
            semconv.ServiceInstanceID(nodeID),
        ),
    )
    if err != nil {
        return nil, err
    }

    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exporter),
        sdktrace.WithResource(res),
    )
    otel.SetTracerProvider(tp)

    return tp.Shutdown, nil
}

var tracer = otel.Tracer("aegis-orchestrator")

// StartSpan creates a new span. Use defer span.End() immediately after.
func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
    return tracer.Start(ctx, name)
}
```

**Instrument the key operations** (per EDD §15.3):

| Span Name | Where to add |
|---|---|
| `task_received` | Communications Gateway inbound `user_task` handler |
| `dedup_check` | Task Dispatcher `HandleInboundTask` before Memory read |
| `policy_validation` | Policy Enforcer `Validate` |
| `decomposition` | Task Dispatcher — wraps planner dispatch through response receipt |
| `plan_execution` | Plan Executor `Execute` (parent of all subtask spans) |
| `subtask_dispatch` | Plan Executor per-subtask dispatch (child span) |
| `subtask_execution` | Plan Executor waiting for subtask result (child span) |
| `result_delivery` | Task Dispatcher final result push to User I/O |
| `recovery_attempt` | Recovery Manager — child span of the subtask span |

Each instrumentation point looks like:
```go
ctx, span := observability.StartSpan(ctx, "decomposition")
defer span.End()
span.SetAttributes(
    attribute.String("task_id", ts.TaskID),
    attribute.String("user_id", ts.UserID),
)
// ... do the work ...
```

On error:
```go
if err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
}
```

**Do NOT set these as span attributes:** raw user input, credential values, task payloads.

**Verification:**
- Start the stack (after Step 8).
- Submit a test task.
- Open Grafana → Explore → Tempo data source.
- Search for the trace_id — you should see a waterfall diagram with nested spans: `task_received` → `dedup_check` → `policy_validation` → `decomposition` → `plan_execution` with subtask children.

---

### Step 8 — Add Grafana stack to docker-compose.yml

**Files to touch:**
- `docker-compose.yml`

**Files to create:**
- `observability/loki-config.yml`
- `observability/promtail-config.yml`
- `observability/tempo-config.yml`
- `observability/grafana-datasources.yml`

#### 8a. `docker-compose.yml` additions

Add these services to your existing compose file:

```yaml
  loki:
    image: grafana/loki:2.9.0
    container_name: aegis-loki
    ports:
      - "3100:3100"
    volumes:
      - ./observability/loki-config.yml:/etc/loki/local-config.yaml
      - loki-data:/loki
    command: -config.file=/etc/loki/local-config.yaml
    networks:
      - aegis-net

  promtail:
    image: grafana/promtail:2.9.0
    container_name: aegis-promtail
    volumes:
      - /var/lib/docker/containers:/var/lib/docker/containers:ro
      - /var/run/docker.sock:/var/run/docker.sock
      - ./observability/promtail-config.yml:/etc/promtail/config.yml
    command: -config.file=/etc/promtail/config.yml
    depends_on:
      - loki
    networks:
      - aegis-net

  tempo:
    image: grafana/tempo:2.3.0
    container_name: aegis-tempo
    ports:
      - "4317:4317"   # OTLP gRPC
      - "3200:3200"   # Tempo HTTP
    volumes:
      - ./observability/tempo-config.yml:/etc/tempo.yml
      - tempo-data:/tmp/tempo
    command: -config.file=/etc/tempo.yml
    networks:
      - aegis-net

  grafana:
    image: grafana/grafana:10.2.0
    container_name: aegis-grafana
    ports:
      - "3000:3000"
    environment:
      - GF_AUTH_ANONYMOUS_ENABLED=true
      - GF_AUTH_ANONYMOUS_ORG_ROLE=Admin
      - GF_AUTH_DISABLE_LOGIN_FORM=true
      - GF_FEATURE_TOGGLES_ENABLE=traceqlEditor
    volumes:
      - ./observability/grafana-datasources.yml:/etc/grafana/provisioning/datasources/datasources.yml
      - grafana-data:/var/lib/grafana
    depends_on:
      - loki
      - tempo
    networks:
      - aegis-net

volumes:
  loki-data:
  tempo-data:
  grafana-data:
```

Make sure the orchestrator service is on the same `aegis-net` network so Tempo is reachable at `tempo:4317`.

#### 8b. `observability/loki-config.yml`

```yaml
auth_enabled: false

server:
  http_listen_port: 3100

common:
  path_prefix: /loki
  storage:
    filesystem:
      chunks_directory: /loki/chunks
      rules_directory: /loki/rules
  replication_factor: 1
  ring:
    instance_addr: 127.0.0.1
    kvstore:
      store: inmemory

schema_config:
  configs:
    - from: 2024-01-01
      store: boltdb-shipper
      object_store: filesystem
      schema: v11
      index:
        prefix: index_
        period: 24h

limits_config:
  allow_structured_metadata: false
  reject_old_samples: false
```

#### 8c. `observability/promtail-config.yml`

**This is the critical file.** It parses JSON logs and extracts `trace_id`, `task_id`, `level`, and `component` as Loki labels so you can query them.

```yaml
server:
  http_listen_port: 9080
  grpc_listen_port: 0

positions:
  filename: /tmp/positions.yaml

clients:
  - url: http://loki:3100/loki/api/v1/push

scrape_configs:
  - job_name: docker
    docker_sd_configs:
      - host: unix:///var/run/docker.sock
        refresh_interval: 5s
    relabel_configs:
      - source_labels: ['__meta_docker_container_name']
        regex: '/(.*)'
        target_label: 'container'
      - source_labels: ['__meta_docker_container_log_stream']
        target_label: 'stream'
    pipeline_stages:
      - json:
          expressions:
            level: level
            component: component
            module: module
            trace_id: trace_id
            task_id: task_id
            plan_id: plan_id
            subtask_id: subtask_id
            msg: msg
      - labels:
          level:
          component:
          module:
          trace_id:
          task_id:
```

**Important:** only promote `level`, `component`, `module`, `trace_id`, and `task_id` to labels. Do not promote `plan_id` or `subtask_id` to labels (they are high-cardinality and would blow up Loki's index). They will still be searchable as parsed fields inside the log JSON.

#### 8d. `observability/tempo-config.yml`

```yaml
server:
  http_listen_port: 3200

distributor:
  receivers:
    otlp:
      protocols:
        grpc:
          endpoint: 0.0.0.0:4317

ingester:
  trace_idle_period: 10s
  max_block_bytes: 1_000_000
  max_block_duration: 5m

compactor:
  compaction:
    block_retention: 1h

storage:
  trace:
    backend: local
    wal:
      path: /tmp/tempo/wal
    local:
      path: /tmp/tempo/blocks
```

#### 8e. `observability/grafana-datasources.yml`

This auto-provisions both data sources AND enables the log→trace link in Grafana, so clicking a `trace_id` in a Loki log line jumps directly to the trace waterfall in Tempo.

```yaml
apiVersion: 1

datasources:
  - name: Loki
    type: loki
    access: proxy
    orgId: 1
    url: http://loki:3100
    basicAuth: false
    isDefault: true
    version: 1
    editable: true
    jsonData:
      derivedFields:
        - datasourceUid: tempo
          matcherRegex: '"trace_id":"(\w+)"'
          name: TraceID
          url: '$${__value.raw}'

  - name: Tempo
    type: tempo
    access: proxy
    orgId: 1
    url: http://tempo:3200
    basicAuth: false
    version: 1
    editable: true
    uid: tempo
    jsonData:
      tracesToLogsV2:
        datasourceUid: loki
        spanStartTimeShift: '-5m'
        spanEndTimeShift: '5m'
        tags: [{ key: 'service.name', value: 'component' }]
        filterByTraceID: true
      serviceMap:
        datasourceUid: prometheus
      nodeGraph:
        enabled: true
```

**Verification:**
- `docker compose up -d loki promtail tempo grafana`
- Open `http://localhost:3000` — Grafana loads with anonymous admin access.
- Go to Connections → Data sources — verify Loki and Tempo are both listed and green.
- Go to Explore → Loki — run query `{component="orchestrator"}` — you should see logs streaming in.

---

### Step 9 — Wire up main.go and add env vars

**Files to touch:**
- `cmd/orchestrator/main.go`
- `internal/config/config.go`

**New config fields:**

```go
type Config struct {
    // ... existing fields ...
    LogLevel     string  // LOG_LEVEL (default: "info")
    LogFormat    string  // LOG_FORMAT (default: "json")
    OTELEndpoint string  // OTEL_EXPORTER_OTLP_ENDPOINT (default: "tempo:4317")
    NodeID       string  // NODE_ID (default: hostname)
}
```

**Update config loader** to read these from environment with the defaults above.

**Update `main.go`:**

```go
func main() {
    cfg := config.Load()

    // Initialize logging FIRST so startup errors are captured
    observability.InitLogger(cfg.LogLevel, cfg.LogFormat, cfg.NodeID)

    ctx := context.Background()

    // Initialize tracing
    tracerShutdown, err := observability.InitTracer(ctx, cfg.OTELEndpoint, cfg.NodeID)
    if err != nil {
        observability.LogFromContext(ctx).Error("failed to initialize tracer", "error", err)
        os.Exit(1)
    }
    defer func() {
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := tracerShutdown(shutdownCtx); err != nil {
            observability.LogFromContext(ctx).Error("tracer shutdown failed", "error", err)
        }
    }()

    observability.LogFromContext(ctx).Info("orchestrator starting",
        "node_id", cfg.NodeID,
        "log_level", cfg.LogLevel,
        "otel_endpoint", cfg.OTELEndpoint)

    // ... rest of startup ...
}
```

**Add env vars to the EDD configuration table** in `docs/EDD.md` §16:

| Variable | Type | Default | Description |
|---|---|---|---|
| `LOG_LEVEL` | enum | `info` | Log verbosity: debug, info, warn, error |
| `LOG_FORMAT` | enum | `json` | Log output format: json (production) or text (local dev) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | host:port | `tempo:4317` | OTLP gRPC endpoint for trace export |

**Verification:**
- `docker compose up -d`
- Check orchestrator startup logs — you should see the JSON "orchestrator starting" line with the new fields.
- `docker compose logs tempo` — no connection errors from the orchestrator.

---

### Step 10 — Add `/debug/trace/{trace_id}` endpoint (demo helper)

**Files to create:**
- `internal/api/debug.go`

**What to build:**

An HTTP endpoint that queries Loki for all logs matching a given trace_id and returns them as a clean JSON timeline. This makes the tracing system visible during the demo without needing Grafana.

```go
package api

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "time"
)

type DebugHandler struct {
    LokiURL string // e.g. "http://loki:3100"
}

type TraceLogEntry struct {
    Timestamp string                 `json:"timestamp"`
    Level     string                 `json:"level"`
    Module    string                 `json:"module"`
    Message   string                 `json:"message"`
    Fields    map[string]interface{} `json:"fields,omitempty"`
}

// GET /debug/trace/{trace_id}
// Returns all log lines for a given trace_id as a JSON timeline.
func (h *DebugHandler) GetTrace(w http.ResponseWriter, r *http.Request) {
    traceID := r.PathValue("trace_id")
    if traceID == "" {
        http.Error(w, "trace_id required", http.StatusBadRequest)
        return
    }

    // Query Loki for all logs with this trace_id in the last hour
    query := fmt.Sprintf(`{component="orchestrator", trace_id="%s"}`, traceID)
    lokiURL := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&limit=1000",
        h.LokiURL,
        url.QueryEscape(query),
        time.Now().Add(-1*time.Hour).UnixNano(),
    )

    resp, err := http.Get(lokiURL)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadGateway)
        return
    }
    defer resp.Body.Close()

    body, _ := io.ReadAll(resp.Body)

    // Parse Loki response and reshape into a clean timeline
    // (Loki returns nested streams; flatten into one chronological list)
    timeline := parseLokiResponse(body)

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "trace_id": traceID,
        "entries":  timeline,
        "count":    len(timeline),
    })
}
```

Register the handler in the orchestrator's HTTP server (same mux as `/health`):

```go
mux.HandleFunc("GET /debug/trace/{trace_id}", debugHandler.GetTrace)
```

**Environment variable:**
- `LOKI_URL` (default: `http://loki:3100`)

**Verification:**
- Submit a test task to the orchestrator.
- Note the trace_id from the startup logs.
- `curl http://localhost:<orchestrator-port>/debug/trace/<trace_id>` — returns a JSON timeline of every log line for that task.

---

## End-to-End Verification Checklist

After all 10 steps are implemented, run this verification:

1. **Bring up the full stack:**
   ```bash
   docker compose up -d
   ```

2. **Submit a multi-step test task:**
   ```bash
   # Publish a test user_task via NATS CLI
   nats pub aegis.orchestrator.tasks.inbound '{
     "message_id": "test-001",
     "message_type": "user_task",
     "source_component": "user_io",
     "correlation_id": "task-demo-001",
     "timestamp": "2026-04-10T14:00:00Z",
     "schema_version": "1.0",
     "payload": {
       "task_id": "task-demo-001",
       "user_id": "test-user",
       "priority": 5,
       "timeout_seconds": 300,
       "payload": {"raw_input": "Book a flight from NYC to LA and find a hotel"},
       "callback_topic": "test.results"
     }
   }'
   ```

3. **Grep the orchestrator logs for the trace_id:**
   ```bash
   docker compose logs orchestrator | grep "task-demo-001" | jq -r '.trace_id' | head -1
   ```
   Copy the trace_id value.

4. **Verify centralized logging in Grafana:**
   - Open `http://localhost:3000` → Explore → Loki
   - Run query: `{trace_id="<your-trace-id>"}`
   - You should see every log line for the task in chronological order.

5. **Verify distributed tracing in Grafana:**
   - Switch data source to Tempo
   - Paste the trace_id into the TraceID search box
   - You should see a waterfall diagram with nested spans:
     - `task_received` (root)
     - `dedup_check`
     - `policy_validation`
     - `decomposition` (with Planner Agent round-trip)
     - `plan_execution`
       - `subtask_dispatch` × N
     - `result_delivery`

6. **Verify log → trace linking:**
   - Back in Loki Explore, click the `TraceID` link next to any log line
   - Grafana should open the corresponding trace in Tempo automatically.

7. **Verify the debug endpoint:**
   ```bash
   curl http://localhost:<orchestrator-port>/debug/trace/<your-trace-id> | jq
   ```
   Returns a clean JSON timeline of all log entries.

---

## Success Criteria

Milestone 2 is complete when:

- [ ] Every orchestrator log line is structured JSON with `trace_id`, `task_id`, `component`, `module`, `node_id`, and `timestamp` fields
- [ ] Every outbound NATS message envelope carries `trace_id`
- [ ] Every inbound NATS message correctly extracts and propagates `trace_id` into the handler's context
- [ ] A single trace_id can be searched in Grafana Loki to see all logs for one request
- [ ] OpenTelemetry traces are visible in Grafana Tempo with nested spans covering the full task lifecycle
- [ ] Clicking a trace_id in Loki jumps to the trace waterfall in Tempo
- [ ] `GET /debug/trace/{trace_id}` returns a chronological log timeline
- [ ] `docker compose up -d` brings up the orchestrator + Loki + Promtail + Tempo + Grafana with no manual configuration
- [ ] No raw user input, credential values, or task payloads appear in any log line or span attribute
- [ ] All existing tests still pass
- [ ] EDD v3.1 §16 configuration table is updated with the new env vars

---

## Notes & Gotchas

- **Context propagation is tedious but critical.** Steps 4 is the biggest chunk of work because it touches every module. Do it methodically: add `ctx` to signatures, update all call sites, let the compiler guide you through the errors.

- **Don't break existing tests.** After Step 4, run `go test ./...` before moving on. Many tests will need to be updated to pass `context.Background()` as the first argument.

- **The Promtail JSON parser is picky.** If logs aren't appearing in Loki, check that the orchestrator is emitting valid single-line JSON to stdout (not text format). `LOG_FORMAT=json` must be set.

- **Tempo needs time to flush.** After submitting a task, wait 10-15 seconds before searching in Tempo — spans are batched before export.

- **High-cardinality labels kill Loki.** Only promote `trace_id` and `task_id` as Loki labels (cardinality bounded). Do NOT promote `plan_id`, `subtask_id`, `message_id`, or any UUID that changes frequently — they'll still be searchable as parsed JSON fields in the log line body.

- **Security reminder (per EDD §15.1):** Never log `raw_input`, credential values, task result payloads, or policy scope tokens. Claude Code should audit every new log line to ensure none of these sneak in.

- **If `OTEL_EXPORTER_OTLP_ENDPOINT` is unreachable, the orchestrator should still start.** The OTLP exporter buffers spans; it should not be a hard startup dependency. Test this by running the orchestrator with Tempo stopped.

---

## Reference Files

- EDD v3.1 §15 — Observability Design (logging, metrics, tracing requirements)
- EDD v3.1 §13.5 — Message envelope security
- EDD v3.1 §16 — Configuration & environment variables