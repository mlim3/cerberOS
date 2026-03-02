package streams

import (
	"time"

	"aegis-databus/pkg/bus"
	"github.com/nats-io/nats.go"
)

const (
	maxAgeSevenDays   = 7 * 24 * time.Hour
	maxBytesTenGB     = 10 * 1024 * 1024 * 1024
	dedupWindow120Sec = 120 * time.Second
)

func EnsureStreams(nc *nats.Conn) error {
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
			Replicas:   3,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamAgents,
			Subjects:   []string{bus.SubjectAgents},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   3,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamRuntime,
			Subjects:   []string{bus.SubjectRuntime},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   3,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamVault,
			Subjects:   []string{bus.SubjectVault},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   3,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamMemory,
			Subjects:   []string{bus.SubjectMemory},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   3,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
		{
			Name:       bus.StreamMonitoring,
			Subjects:   []string{bus.SubjectMonitoring},
			MaxAge:     maxAgeSevenDays,
			MaxBytes:   maxBytesTenGB,
			Replicas:   3,
			Discard:    nats.DiscardOld,
			Duplicates: dedupWindow120Sec,
		},
	}

	for _, cfg := range configs {
		_, err := js.StreamInfo(cfg.Name)
		if err == nil {
			if _, err := js.UpdateStream(&cfg); err != nil {
				return err
			}
			continue
		}
		if err != nats.ErrStreamNotFound {
			return err
		}
		if _, err := js.AddStream(&cfg); err != nil {
			return err
		}
	}

	return nil
}
