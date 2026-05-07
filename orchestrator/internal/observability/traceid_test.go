package observability

import (
	"strings"
	"testing"
)

func TestNormalizeTraceIDForW3C(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"not-hex", ""},
		{"0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef"},
		{"01234567-89ab-cdef-0123-456789abcdef", "0123456789abcdef0123456789abcdef"},
	}
	for _, tc := range cases {
		got := NormalizeTraceIDForW3C(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeTraceIDForW3C(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewRandomTraceID_Format(t *testing.T) {
	t.Parallel()
	id := NewRandomTraceID()
	if len(id) != 32 {
		t.Fatalf("len = %d, want 32", len(id))
	}
	if id != strings.ToLower(id) {
		t.Fatalf("want lowercase: %q", id)
	}
	for _, c := range id {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		t.Fatalf("non-hex in %q", id)
	}
}

func TestTraceparentForOutgoingHTTP(t *testing.T) {
	t.Parallel()
	tp := TraceparentForOutgoingHTTP("0123456789abcdef0123456789abcdef")
	if tp == "" {
		t.Fatal("empty traceparent")
	}
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" || parts[1] != "0123456789abcdef0123456789abcdef" || len(parts[2]) != 16 || parts[3] != "01" {
		t.Fatalf("unexpected traceparent: %q", tp)
	}
}
