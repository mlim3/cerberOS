package observability

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// NewRandomTraceID returns a W3C trace_id: 32 lowercase hexadecimal characters
// (16 random bytes). Matches the format produced by io-api and recommended for LogQL.
func NewRandomTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NormalizeTraceIDForW3C returns a 32-char lowercase hex trace_id when the input
// is a UUID (with or without dashes) or already 32 hex chars; otherwise "".
func NormalizeTraceIDForW3C(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return ""
	}
	for i := 0; i < 32; i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return ""
	}
	return s
}

// TraceparentForOutgoingHTTP builds a W3C traceparent header for downstream HTTP
// (e.g. IO API trace middleware). Returns "" if traceID cannot be normalized.
func TraceparentForOutgoingHTTP(traceID string) string {
	tid := NormalizeTraceIDForW3C(traceID)
	if tid == "" {
		return ""
	}
	span := make([]byte, 8)
	_, _ = rand.Read(span)
	return fmt.Sprintf("00-%s-%s-01", tid, hex.EncodeToString(span))
}
