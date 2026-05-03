package logfields

import (
	"strings"
	"testing"
)

func TestPreviewWords(t *testing.T) {
	cases := []struct {
		name string
		in   string
		maxW int
		maxC int
		want string
	}{
		{name: "empty", in: "", maxW: 20, maxC: 140, want: ""},
		{name: "short", in: "hello world", maxW: 20, maxC: 140, want: "hello world"},
		{name: "collapses whitespace", in: "  hello\n\nworld\t\t!  ", maxW: 20, maxC: 140, want: "hello world !"},
		{name: "word truncate", in: "one two three four five six", maxW: 3, maxC: 140, want: "one two three…"},
		{name: "char truncate", in: "abcdefghij", maxW: 20, maxC: 5, want: "abcde…"},
		{name: "no trailing space before ellipsis", in: "one two three     four five", maxW: 3, maxC: 140, want: "one two three…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PreviewWords(tc.in, tc.maxW, tc.maxC)
			if got != tc.want {
				t.Fatalf("PreviewWords(%q, %d, %d) = %q, want %q", tc.in, tc.maxW, tc.maxC, got, tc.want)
			}
		})
	}
}

func TestPreviewHeadTail(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := PreviewHeadTail("", 15, 10); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})

	t.Run("under threshold returns full", func(t *testing.T) {
		in := "one two three four five"
		if got := PreviewHeadTail(in, 15, 10); got != in {
			t.Fatalf("expected full passthrough, got %q", got)
		}
	})

	t.Run("collapses whitespace under threshold", func(t *testing.T) {
		in := "  one\ttwo\n\nthree   four  "
		want := "one two three four"
		if got := PreviewHeadTail(in, 15, 10); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("head and tail with omitted middle", func(t *testing.T) {
		// 30 unique words; head=3, tail=2 → 25 omitted words plus their separators.
		var b strings.Builder
		for i := 0; i < 30; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("w")
			b.WriteString(itoa(i))
		}
		in := b.String()
		got := PreviewHeadTail(in, 3, 2)
		// must start with the first three words and end with the last two
		if !strings.HasPrefix(got, "w0 w1 w2 ") {
			t.Fatalf("expected head 'w0 w1 w2', got %q", got)
		}
		if !strings.HasSuffix(got, " w28 w29") {
			t.Fatalf("expected tail 'w28 w29', got %q", got)
		}
		// must contain the omission marker
		if !strings.Contains(got, "[..") || !strings.Contains(got, " chars..]") {
			t.Fatalf("expected omission marker, got %q", got)
		}
	})

	t.Run("exact head+tail boundary returns full", func(t *testing.T) {
		// 25 words with headWords=15 tailWords=10 → 25 == headWords+tailWords → full
		var b strings.Builder
		for i := 0; i < 25; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("w")
			b.WriteString(itoa(i))
		}
		in := b.String()
		if got := PreviewHeadTail(in, 15, 10); got != in {
			t.Fatalf("expected full passthrough at boundary, got %q", got)
		}
	})

	t.Run("zero values fall back to defaults", func(t *testing.T) {
		// 30 words; (0,0) should become (15,10) → 30 > 25 so it should truncate.
		var b strings.Builder
		for i := 0; i < 30; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("w")
			b.WriteString(itoa(i))
		}
		in := b.String()
		got := PreviewHeadTail(in, 0, 0)
		if got == in {
			t.Fatalf("expected truncation with default head/tail, got passthrough: %q", got)
		}
		if !strings.Contains(got, "[..") {
			t.Fatalf("expected omission marker with default head/tail, got %q", got)
		}
	})
}

func TestBoundedDetails(t *testing.T) {
	t.Run("nil passthrough", func(t *testing.T) {
		if got := BoundedDetails(nil); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("short values pass through", func(t *testing.T) {
		in := map[string]interface{}{
			"request_id":   "abc-123",
			"status":       "ok",
			"count":        42,
			"latency_ms":   1234,
			"is_error":     false,
			"short_string": strings.Repeat("a", boundedDetailsThreshold-1),
		}
		out := BoundedDetails(in)
		for k, v := range in {
			if got := out[k]; got != v {
				t.Fatalf("%s: expected passthrough %v, got %v", k, v, got)
			}
		}
	})

	t.Run("long string is head+tailed", func(t *testing.T) {
		long := strings.Repeat("word ", 100) // ~500 chars, ~100 words
		in := map[string]interface{}{
			"final_result": long,
			"request_id":   "abc-123",
		}
		out := BoundedDetails(in)
		if out["request_id"] != "abc-123" {
			t.Fatalf("short field was changed: %v", out["request_id"])
		}
		got, ok := out["final_result"].(string)
		if !ok {
			t.Fatalf("final_result is not a string: %T", out["final_result"])
		}
		if !strings.Contains(got, "[..") || !strings.Contains(got, " chars..]") {
			t.Fatalf("expected head+tail marker, got %q", got)
		}
		if len(got) >= len(long) {
			t.Fatalf("expected truncation, length stayed %d", len(got))
		}
	})

	t.Run("long []byte is bounded", func(t *testing.T) {
		long := []byte(strings.Repeat("word ", 100))
		out := BoundedDetails(map[string]interface{}{"body": long})
		got, ok := out["body"].(string)
		if !ok {
			t.Fatalf("expected []byte to be coerced to string, got %T", out["body"])
		}
		if !strings.Contains(got, "[..") {
			t.Fatalf("expected head+tail marker, got %q", got)
		}
	})

	t.Run("nested maps are recursed", func(t *testing.T) {
		long := strings.Repeat("word ", 100)
		out := BoundedDetails(map[string]interface{}{
			"outer": map[string]interface{}{
				"inner_result": long,
			},
		})
		nested, ok := out["outer"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected nested map, got %T", out["outer"])
		}
		got := nested["inner_result"].(string)
		if !strings.Contains(got, "[..") {
			t.Fatalf("nested long string was not bounded, got %q", got)
		}
	})
}

// itoa avoids fmt for tight tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
