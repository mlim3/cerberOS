package audit_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mlim3/cerberOS/vault/engine/audit"
)

// captureExporter records exported events for assertions.
type captureExporter struct {
	events []audit.Event
}

func (c *captureExporter) Export(e audit.Event) error {
	c.events = append(c.events, e)
	return nil
}

// failExporter always returns an error.
type failExporter struct {
	err error
}

func (f *failExporter) Export(audit.Event) error { return f.err }

func TestLogger_Log(t *testing.T) {
	t.Run("SetsTimeIfZero", func(t *testing.T) {
		cap := &captureExporter{}
		logger := audit.New(cap)

		before := time.Now().UTC()
		logger.Log(audit.Event{Kind: audit.KindInfo, Message: "test"})
		after := time.Now().UTC()

		if len(cap.events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(cap.events))
		}
		ev := cap.events[0]
		if ev.Time.Before(before) || ev.Time.After(after) {
			t.Fatalf("time %v not between %v and %v", ev.Time, before, after)
		}
	})

	t.Run("PreservesExplicitTime", func(t *testing.T) {
		cap := &captureExporter{}
		logger := audit.New(cap)

		fixed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		logger.Log(audit.Event{Time: fixed, Kind: audit.KindInfo})

		if cap.events[0].Time != fixed {
			t.Fatalf("time = %v, want %v", cap.events[0].Time, fixed)
		}
	})

	t.Run("FansOutToMultipleExporters", func(t *testing.T) {
		c1 := &captureExporter{}
		c2 := &captureExporter{}
		logger := audit.New(c1, c2)

		logger.Log(audit.Event{Kind: audit.KindInfo, Message: "fan-out"})

		if len(c1.events) != 1 || len(c2.events) != 1 {
			t.Fatalf("expected 1 event each, got %d and %d", len(c1.events), len(c2.events))
		}
	})

	t.Run("ZeroValueLogger_NoPanic", func(t *testing.T) {
		logger := &audit.Logger{}
		// Should not panic
		logger.Log(audit.Event{Kind: audit.KindInfo, Message: "dropped"})
	})
}

func TestJSONExporter(t *testing.T) {
	t.Run("WritesValidJSON", func(t *testing.T) {
		var buf bytes.Buffer
		exp := audit.NewJSONExporter(&buf)

		ev := audit.Event{
			Time:    time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
			Kind:    audit.KindSecretAccess,
			Agent:   "test-agent",
			Keys:    []string{"KEY1"},
			Message: "accessed",
		}
		if err := exp.Export(ev); err != nil {
			t.Fatal(err)
		}

		var decoded audit.Event
		if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
			t.Fatalf("invalid JSON: %v\nraw: %s", err, buf.String())
		}
		if decoded.Agent != "test-agent" {
			t.Fatalf("agent = %q", decoded.Agent)
		}
		if decoded.Kind != audit.KindSecretAccess {
			t.Fatalf("kind = %q", decoded.Kind)
		}
	})

	t.Run("NewlineDelimited", func(t *testing.T) {
		var buf bytes.Buffer
		exp := audit.NewJSONExporter(&buf)

		exp.Export(audit.Event{Kind: audit.KindInfo, Message: "first"})
		exp.Export(audit.Event{Kind: audit.KindDebug, Message: "second"})

		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
		}
		for i, line := range lines {
			var ev audit.Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("line %d invalid JSON: %v", i, err)
			}
		}
	})

	t.Run("AllFieldsSerialized", func(t *testing.T) {
		var buf bytes.Buffer
		exp := audit.NewJSONExporter(&buf)

		ev := audit.Event{
			Time:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Kind:    audit.KindError,
			Agent:   "ag",
			Keys:    []string{"K1", "K2"},
			Message: "msg",
			Error:   "err",
			Warning: "warn",
			Info:    "info",
			Debug:   "dbg",
			Trace:   "trc",
		}
		exp.Export(ev)

		raw := buf.String()
		for _, field := range []string{`"kind"`, `"agent"`, `"keys"`, `"message"`, `"error"`, `"warning"`, `"info"`, `"debug"`, `"trace"`} {
			if !strings.Contains(raw, field) {
				t.Errorf("missing field %s in JSON: %s", field, raw)
			}
		}
	})

	t.Run("OmitsEmptyKeys", func(t *testing.T) {
		var buf bytes.Buffer
		exp := audit.NewJSONExporter(&buf)

		exp.Export(audit.Event{Kind: audit.KindInfo, Message: "no keys"})

		raw := buf.String()
		if strings.Contains(raw, `"keys"`) {
			t.Fatalf("expected keys to be omitted, got: %s", raw)
		}
	})
}

func TestMultiExporter(t *testing.T) {
	t.Run("FansOutToAll", func(t *testing.T) {
		c1 := &captureExporter{}
		c2 := &captureExporter{}
		c3 := &captureExporter{}
		multi := audit.NewMultiExporter(c1, c2, c3)

		multi.Export(audit.Event{Kind: audit.KindInfo})

		for i, c := range []*captureExporter{c1, c2, c3} {
			if len(c.events) != 1 {
				t.Errorf("exporter %d: got %d events", i, len(c.events))
			}
		}
	})

	t.Run("ContinuesOnError", func(t *testing.T) {
		c1 := &failExporter{err: fmt.Errorf("boom")}
		c2 := &captureExporter{}
		multi := audit.NewMultiExporter(c1, c2)

		multi.Export(audit.Event{Kind: audit.KindInfo})

		if len(c2.events) != 1 {
			t.Fatal("second exporter did not receive event after first failed")
		}
	})

	t.Run("ReturnsLastError", func(t *testing.T) {
		f1 := &failExporter{err: fmt.Errorf("first")}
		f2 := &failExporter{err: fmt.Errorf("last")}
		multi := audit.NewMultiExporter(f1, f2)

		err := multi.Export(audit.Event{Kind: audit.KindInfo})
		if err == nil || err.Error() != "last" {
			t.Fatalf("err = %v, want 'last'", err)
		}
	})
}

func TestEventKinds(t *testing.T) {
	kinds := []audit.EventKind{
		audit.KindSecretAccess,
		audit.KindInjection,
		audit.KindError,
		audit.KindWarning,
		audit.KindInfo,
		audit.KindDebug,
		audit.KindTrace,
	}

	seen := make(map[audit.EventKind]bool)
	for _, k := range kinds {
		if k == "" {
			t.Errorf("empty EventKind constant")
		}
		if seen[k] {
			t.Errorf("duplicate EventKind: %q", k)
		}
		seen[k] = true
	}
}
