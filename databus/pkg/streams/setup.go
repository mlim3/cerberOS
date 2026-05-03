package streams

import (
	"context"
	"time"

	"aegis-databus/pkg/bus"
	"github.com/nats-io/nats.go"
)

const (
	maxAgeSevenDays    = 7 * 24 * time.Hour
	maxAgeTransient    = 1 * time.Hour // EDD §8.1: capability / vault progress (ephemeral interest)
	maxBytesTenGB      = 10 * 1024 * 1024 * 1024
	maxBytesTransient  = 256 * 1024 * 1024
	dedupWindow120Sec  = 120 * time.Second
	// Per-stream JetStream API budget (cluster Raft can exceed default client timeout).
	jsOpTimeout = 3 * time.Minute
)

// EnsureStreams creates or updates Aegis JetStream streams (3 replicas for cluster).
// Durable domains use file storage and 7-day retention; transient domains use memory storage
// and short retention (EDD §8.1 — at-most-once style, no long-lived replay).
func EnsureStreams(nc *nats.Conn) error {
	return EnsureStreamsWithReplicas(nc, 3)
}

// EnsureStreamsWithReplicas is for single-node tests; use replicas=1.
func EnsureStreamsWithReplicas(nc *nats.Conn, replicas int) error {
	js, err := nc.JetStream()
	if err != nil {
		return err
	}

	configs := []nats.StreamConfig{
		{
			Name:       bus.StreamTasks,
			Subjects:   []string{bus.SubjectTasks},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamUI,
			Subjects:   []string{bus.SubjectUI},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamAgents,
			Subjects:   []string{bus.SubjectAgents},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamRuntime,
			Subjects:   []string{bus.SubjectRuntime},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamVault,
			Subjects:   []string{bus.SubjectVault},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamMemory,
			Subjects:   []string{bus.SubjectMemory},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamMonitoring,
			Subjects:   []string{bus.SubjectMonitoring},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamDLQ,
			Subjects:   []string{bus.SubjectDLQ, bus.SubjectDLQPattern},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   1 * 1024 * 1024 * 1024, // 1 GB
			Replicas:   replicas,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:      bus.StreamCapabilityTransient,
			Subjects:  []string{bus.SubjectCapability},
			Storage:   nats.MemoryStorage,
			MaxAge:    maxAgeTransient,
			MaxBytes:  maxBytesTransient,
			Replicas:  replicas,
			Discard:   nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:      bus.StreamVaultProgressTransient,
			Subjects:  []string{bus.SubjectVaultProgress},
			Storage:   nats.MemoryStorage,
			MaxAge:    maxAgeTransient,
			MaxBytes:  maxBytesTransient,
			Replicas:  replicas,
			Discard:   nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
	}

	for _, cfg := range configs {
		opCtx, cancel := context.WithTimeout(context.Background(), jsOpTimeout)
		jsOpt := nats.Context(opCtx)
		_, err := js.StreamInfo(cfg.Name, jsOpt)
		if err == nil {
			_, err = js.UpdateStream(&cfg, jsOpt)
			cancel()
			if err != nil {
				return err
			}
			continue
		}
		if err != nats.ErrStreamNotFound {
			cancel()
			return err
		}
		_, err = js.AddStream(&cfg, jsOpt)
		cancel()
		if err != nil {
			return err
		}
	}

	return nil
}
