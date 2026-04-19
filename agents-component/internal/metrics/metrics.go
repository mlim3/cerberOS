// Package metrics exposes a Prometheus metrics endpoint for the aegis-agents
// component. All instrumentation hooks are injected into other packages via
// plain function values to avoid import-cycle risks — only this package and
// cmd/aegis-agents/main.go import the prometheus client library.
package metrics

import (
	"net/http"
	"time"

	"github.com/cerberOS/agents-component/internal/comms"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "aegis_agents"

// Recorder holds all Prometheus metric descriptors for the aegis-agents
// component. Construct with NewRecorder; pass observer methods as hooks to
// other packages at wiring time.
type Recorder struct {
	reg *prometheus.Registry

	// Agent state gauges — one time-series per lifecycle state.
	agentsByState *prometheus.GaugeVec

	// Lifecycle event counters — spawn, terminate, recover.
	lifecycleTotal *prometheus.CounterVec

	// Skill invocation counter — keyed by domain and command.
	skillInvocationsTotal *prometheus.CounterVec

	// Vault execute latency histogram — keyed by operation_type.
	vaultExecuteDurationMs *prometheus.HistogramVec

	// NATS publish and consume latency histograms — keyed by subject.
	natsPublishDurationMs *prometheus.HistogramVec
	natsConsumeDurationMs *prometheus.HistogramVec

	// Context management event counters.
	compactionTriggeredTotal prometheus.Counter
	contextOverflowTotal     prometheus.Counter

	// LLM response cache counters — per Assignment #9 "LLM caching:
	// Personalization". Incremented via MetricsEvent from agent-process.
	llmCacheHitsTotal   prometheus.Counter
	llmCacheMissesTotal prometheus.Counter
}

// NewRecorder creates and registers all metrics in a fresh Prometheus registry.
// Caller is responsible for serving Handler() on the /metrics path.
func NewRecorder() *Recorder {
	reg := prometheus.NewRegistry()

	r := &Recorder{
		reg: reg,

		agentsByState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "agents_by_state",
			Help:      "Current number of registered agents in each lifecycle state.",
		}, []string{"state"}),

		lifecycleTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "lifecycle_events_total",
			Help:      "Total lifecycle events by type (spawn, terminate, recover).",
		}, []string{"event"}),

		skillInvocationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "skill_invocations_total",
			Help:      "Total number of skill spec queries by domain and command.",
		}, []string{"domain", "command"}),

		vaultExecuteDurationMs: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "vault_execute_duration_ms",
			Help:      "Vault execute operation latency in milliseconds by operation_type.",
			Buckets:   []float64{10, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
		}, []string{"operation_type"}),

		natsPublishDurationMs: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "nats_publish_duration_ms",
			Help:      "NATS publish call duration in milliseconds by subject.",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50},
		}, []string{"subject"}),

		natsConsumeDurationMs: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "nats_consume_duration_ms",
			Help:      "NATS message handler execution latency in milliseconds by subject.",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50},
		}, []string{"subject"}),

		compactionTriggeredTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "context_compaction_triggered_total",
			Help:      "Total context compaction events triggered across all agent processes.",
		}),

		contextOverflowTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "context_overflow_total",
			Help:      "Total CONTEXT_OVERFLOW hard aborts across all agent processes.",
		}),

		llmCacheHitsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "llm_cache_hits_total",
			Help:      "Total LLM response cache hits across all agent processes.",
		}),

		llmCacheMissesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "llm_cache_misses_total",
			Help:      "Total LLM response cache misses across all agent processes.",
		}),
	}

	reg.MustRegister(
		r.agentsByState,
		r.lifecycleTotal,
		r.skillInvocationsTotal,
		r.vaultExecuteDurationMs,
		r.natsPublishDurationMs,
		r.natsConsumeDurationMs,
		r.compactionTriggeredTotal,
		r.contextOverflowTotal,
		r.llmCacheHitsTotal,
		r.llmCacheMissesTotal,
	)

	// Pre-initialise all state label values so every state appears in the
	// exposition output from the first scrape, even before any transitions.
	for _, s := range []string{
		"pending", "spawning", "active", "idle",
		"suspended", "recovering", "terminated",
	} {
		r.agentsByState.WithLabelValues(s).Set(0)
	}
	for _, e := range []string{"spawn", "terminate", "recover"} {
		r.lifecycleTotal.WithLabelValues(e)
	}

	return r
}

// Handler returns an http.Handler for the /metrics endpoint.
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.reg, promhttp.HandlerOpts{})
}

// — Observer methods — these are passed as hooks to other packages at wiring
// time so those packages do not need to import prometheus directly.

// ObserveStateChange updates the agents_by_state gauge after a state mutation.
//
//   - from==""  signals initial agent registration (increment to only).
//   - to==""    signals deregistration (decrement from only).
//   - Both non-empty: normal state transition.
func (r *Recorder) ObserveStateChange(from, to string) {
	if from != "" {
		r.agentsByState.WithLabelValues(from).Dec()
	}
	if to != "" {
		r.agentsByState.WithLabelValues(to).Inc()
	}
}

// ObserveLifecycleEvent increments the lifecycle_events_total counter.
// event must be one of "spawn", "terminate", "recover".
func (r *Recorder) ObserveLifecycleEvent(event string) {
	r.lifecycleTotal.WithLabelValues(event).Inc()
}

// ObserveSkillInvocation increments the skill_invocations_total counter.
func (r *Recorder) ObserveSkillInvocation(domain, command string) {
	r.skillInvocationsTotal.WithLabelValues(domain, command).Inc()
}

// ObserveVaultExecute records a vault execute latency sample.
func (r *Recorder) ObserveVaultExecute(operationType string, elapsedMS int) {
	r.vaultExecuteDurationMs.WithLabelValues(operationType).Observe(float64(elapsedMS))
}

// ObserveNATSPublish records a NATS publish duration sample.
func (r *Recorder) ObserveNATSPublish(subject string, elapsed time.Duration) {
	ms := float64(elapsed.Nanoseconds()) / 1e6
	r.natsPublishDurationMs.WithLabelValues(subject).Observe(ms)
}

// ObserveNATSConsume records a message handler execution duration sample.
func (r *Recorder) ObserveNATSConsume(subject string, elapsed time.Duration) {
	ms := float64(elapsed.Nanoseconds()) / 1e6
	r.natsConsumeDurationMs.WithLabelValues(subject).Observe(ms)
}

// IncCompactionTriggered increments the context_compaction_triggered_total counter.
func (r *Recorder) IncCompactionTriggered() {
	r.compactionTriggeredTotal.Inc()
}

// IncContextOverflow increments the context_overflow_total counter.
func (r *Recorder) IncContextOverflow() {
	r.contextOverflowTotal.Inc()
}

// IncLLMCacheHit increments the llm_cache_hits_total counter.
func (r *Recorder) IncLLMCacheHit() {
	r.llmCacheHitsTotal.Inc()
}

// IncLLMCacheMiss increments the llm_cache_misses_total counter.
func (r *Recorder) IncLLMCacheMiss() {
	r.llmCacheMissesTotal.Inc()
}

// — Instrumented comms.Client wrapper ————————————————————————————————————————

// instrumentedComms wraps a comms.Client and records publish/consume latencies.
type instrumentedComms struct {
	inner    comms.Client
	recorder *Recorder
}

// WrapComms returns a comms.Client that measures publish and handler latencies
// and forwards them to r. All other behaviour is delegated to c unchanged.
func WrapComms(c comms.Client, r *Recorder) comms.Client {
	return &instrumentedComms{inner: c, recorder: r}
}

func (ic *instrumentedComms) Publish(subject string, opts comms.PublishOptions, payload interface{}) error {
	start := time.Now()
	err := ic.inner.Publish(subject, opts, payload)
	ic.recorder.ObserveNATSPublish(subject, time.Since(start))
	return err
}

func (ic *instrumentedComms) Subscribe(subject string, handler comms.MessageHandler) error {
	return ic.inner.Subscribe(subject, func(msg *comms.Message) {
		start := time.Now()
		handler(msg)
		ic.recorder.ObserveNATSConsume(subject, time.Since(start))
	})
}

func (ic *instrumentedComms) SubscribeDurable(subject, durable string, handler comms.MessageHandler) error {
	return ic.inner.SubscribeDurable(subject, durable, func(msg *comms.Message) {
		start := time.Now()
		handler(msg)
		ic.recorder.ObserveNATSConsume(subject, time.Since(start))
	})
}

func (ic *instrumentedComms) EnsureStreams() error {
	return ic.inner.EnsureStreams()
}

func (ic *instrumentedComms) Close() error {
	return ic.inner.Close()
}
