package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// auditEvent is the structured record written for every secret resolution.
type auditEvent struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Keys   []string  `json:"keys"`
	Source string    `json:"source"` // remote address of the caller
}

// auditExporter decides what to do with an auditEvent.
type auditExporter interface {
	export(e auditEvent) error
}

// auditLogger fans events out to one or more exporters.
type auditLogger struct {
	exporters []auditExporter
}

func newAuditLogger(exporters ...auditExporter) *auditLogger {
	return &auditLogger{exporters: exporters}
}

func (l *auditLogger) log(e auditEvent) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	for _, exp := range l.exporters {
		if err := exp.export(e); err != nil {
			fmt.Fprintf(os.Stderr, "audit: exporter %T: %v\n", exp, err)
		}
	}
}

// jsonAuditExporter writes newline-delimited JSON to any io.Writer.
type jsonAuditExporter struct{ w io.Writer }

func newJSONAuditExporter(w io.Writer) *jsonAuditExporter {
	return &jsonAuditExporter{w: w}
}

func (e *jsonAuditExporter) export(ev auditEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.w, "%s\n", b)
	return err
}
