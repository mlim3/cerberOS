package observability

import (
	"context"
	"testing"
)

func TestNormalizeOTLPGRPCEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"tempo:4317", "tempo:4317"},
		{"http://tempo:4317", "tempo:4317"},
		{"https://tempo:4317/v1/traces", "tempo:4317"},
		{"  localhost:4317  ", "localhost:4317"},
	}
	for _, tc := range cases {
		got := NormalizeOTLPGRPCEndpoint(tc.in)
		if got != tc.want {
			t.Fatalf("NormalizeOTLPGRPCEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInitTracer_DisabledWhenEndpointEmpty(t *testing.T) {
	t.Parallel()
	shutdown, err := InitTracer(context.Background(), "  ", "test-node")
	if err != nil {
		t.Fatalf("InitTracer: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown is nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
