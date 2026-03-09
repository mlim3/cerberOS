// Package audit provides a pluggable structured audit-logging interface for
// agent-driven secret access. Callers emit Events; Exporters decide what to
// do with them (stdout, file, HTTP webhook, etc.).
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// EventKind categorises an audit event so consumers can filter cheaply.
type EventKind string

const (
	// KindSecretAccess is emitted whenever an agent's script resolves one or
	// more secret placeholders.
	KindSecretAccess EventKind = "secret_access"
	// KindExecution is emitted when an agent submits a script for execution.
	KindExecution EventKind = "execution"
)

// Event is the unit of information written to every Exporter.
type Event struct {
	Time    time.Time `json:"time"`
	Kind    EventKind `json:"kind"`
	Agent   string    `json:"agent"`
	// Keys holds the secret names requested (never the values).
	Keys    []string  `json:"keys,omitempty"`
	Message string    `json:"message,omitempty"`
}

// Exporter is the single extension point: implement this interface to send
// audit events anywhere — stdout, a log file, Splunk, a webhook, etc.
type Exporter interface {
	Export(e Event) error
}

// Logger dispatches Events to one or more Exporters.
// The zero value is safe but drops all events; call New to attach exporters.
type Logger struct {
	exporters []Exporter
}

// New returns a Logger that fans events out to all provided Exporters.
func New(exporters ...Exporter) *Logger {
	return &Logger{exporters: exporters}
}

// Log emits an event to every registered Exporter.
// Errors from individual exporters are printed to stderr but do not stop
// delivery to the remaining exporters.
func (l *Logger) Log(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	for _, exp := range l.exporters {
		if err := exp.Export(e); err != nil {
			fmt.Fprintf(os.Stderr, "audit: exporter %T: %v\n", exp, err)
		}
	}
}

// --- built-in exporters -----------------------------------------------------

// JSONExporter writes one JSON object per event to any io.Writer.
type JSONExporter struct {
	w io.Writer
}

// NewJSONExporter returns an exporter that writes newline-delimited JSON to w.
// Pass os.Stdout for human-readable console output, or an *os.File for a log.
func NewJSONExporter(w io.Writer) *JSONExporter {
	return &JSONExporter{w: w}
}

func (e *JSONExporter) Export(ev Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.w, "%s\n", b)
	return err
}

// MultiExporter fans one event out to a slice of Exporters.
// Useful for combining e.g. stdout + file exporters without changing callers.
type MultiExporter struct {
	exporters []Exporter
}

// NewMultiExporter combines several Exporters into one.
func NewMultiExporter(exporters ...Exporter) *MultiExporter {
	return &MultiExporter{exporters: exporters}
}

func (m *MultiExporter) Export(ev Event) error {
	var last error
	for _, exp := range m.exporters {
		if err := exp.Export(ev); err != nil {
			fmt.Fprintf(os.Stderr, "audit: exporter %T: %v\n", exp, err)
			last = err
		}
	}
	return last
}
