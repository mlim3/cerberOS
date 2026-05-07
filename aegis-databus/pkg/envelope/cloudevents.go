package envelope

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidEnvelope = errors.New("invalid CloudEvent envelope")

// CloudEvent represents CloudEvents 1.0 envelope.
// TraceID supports Design Principle 4 (observable operations / tracing).
type CloudEvent struct {
	SpecVersion     string      `json:"specversion"`
	ID              string      `json:"id"`
	Source          string      `json:"source"`
	Type            string      `json:"type"`
	Time            string      `json:"time"`
	CorrelationID   string      `json:"correlationid"`
	TraceID         string      `json:"traceid,omitempty"` // W3C Trace Context or custom trace ID
	DataContentType string      `json:"datacontenttype"`
	Data            interface{} `json:"data"`
}

// ExtractID returns the CloudEvents id from JSON payload, or empty string if not found.
// Used for deduplication (Nats-Msg-Id) and idempotency checks when replaying from DLQ.
func ExtractID(data []byte) string {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if id, ok := m["id"].(string); ok {
		return id
	}
	return ""
}

// ParseMetadata extracts source, correlationid, and traceid from CloudEvents JSON.
// Used for audit logging — never log payload content.
// traceid supports Design Principle 4 (observable operations / tracing).
func ParseMetadata(data []byte) (source, correlationID, traceID string) {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", "", ""
	}
	if s, ok := m["source"].(string); ok {
		source = s
	}
	if c, ok := m["correlationid"].(string); ok {
		correlationID = c
	}
	if t, ok := m["traceid"].(string); ok {
		traceID = t
	}
	return source, correlationID, traceID
}

// Validate returns nil if the envelope is valid CloudEvents 1.0.
func Validate(data []byte) error {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	if v, ok := m["specversion"].(string); !ok || v != "1.0" {
		return fmt.Errorf("%w: missing or invalid specversion", ErrInvalidEnvelope)
	}
	if _, ok := m["id"]; !ok {
		return fmt.Errorf("%w: missing required id", ErrInvalidEnvelope)
	}
	if _, ok := m["source"]; !ok {
		return fmt.Errorf("%w: missing required source", ErrInvalidEnvelope)
	}
	if _, ok := m["type"]; !ok {
		return fmt.Errorf("%w: missing required type", ErrInvalidEnvelope)
	}
	return nil
}

// Build creates a valid CloudEvent with defaults.
// Optionally pass a traceID for distributed tracing (Design Principle 4).
func Build(source, eventType string, data interface{}) CloudEvent {
	return CloudEvent{
		SpecVersion:     "1.0",
		ID:              NewID(),
		Source:          source,
		Type:            eventType,
		Time:            time.Now().UTC().Format(time.RFC3339Nano),
		CorrelationID:   NewID(),
		TraceID:         "", // Set via SetTraceID for observable flows
		DataContentType: "application/json",
		Data:            data,
	}
}

// SetTraceID sets the trace ID for distributed tracing.
func (e *CloudEvent) SetTraceID(traceID string) {
	e.TraceID = traceID
}

// MustMarshal returns JSON bytes or panics (use only in controlled paths).
func (e CloudEvent) MustMarshal() []byte {
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return b
}

// SetCorrelationID sets the correlation ID for request-reply linking.
func (e *CloudEvent) SetCorrelationID(cid string) {
	e.CorrelationID = cid
}

// NewID returns a new UUID-style ID for CloudEvents.
func NewID() string {
	return newID()
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}
