// Package logfields holds shared helpers for building safe log field values
// across the agents-component. The canonical logging schema is defined in
// docs/logging.md.
package logfields

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// PreviewWords truncates user-supplied text into a debug-safe preview suitable
// for short metadata fields (titles, reasons, error codes, progress messages,
// voice transcripts).
//
// Caps at maxWords words AND maxChars characters (whichever is hit first) and
// appends "…" when truncation occurred. Whitespace is collapsed and trimmed so
// multi-line input renders as a single readable string.
//
// For long *conversation* content (user chat messages, agent replies), prefer
// PreviewHeadTail — it keeps the start AND the end so the line remains
// recognisable when the message is many paragraphs long. See docs/logging.md
// for the full policy.
func PreviewWords(s string, maxWords, maxChars int) string {
	if maxWords <= 0 {
		maxWords = 20
	}
	if maxChars <= 0 {
		maxChars = 140
	}
	flat := strings.Join(strings.Fields(s), " ")
	if flat == "" {
		return ""
	}
	words := strings.Split(flat, " ")
	truncated := false
	out := flat
	if len(words) > maxWords {
		out = strings.Join(words[:maxWords], " ")
		truncated = true
	}
	if utf8.RuneCountInString(out) > maxChars {
		runes := []rune(out)
		out = strings.TrimRight(string(runes[:maxChars]), " ")
		truncated = true
	}
	if truncated {
		return out + "…"
	}
	return out
}

// boundedDetailsThreshold is the per-string char threshold at which
// BoundedDetails switches a value over to PreviewHeadTail. Short strings
// (request IDs, status enums, short error codes, key names) pass through
// unchanged; long ones (tool output, agent results, raw bodies) are
// truncated head+tail so the log line stays readable.
const boundedDetailsThreshold = 200

// BoundedDetails returns a debug-safe copy of an arbitrary
// map[string]interface{} suitable for passing as a single slog attribute.
// Long string values are replaced with the PreviewHeadTail format; long
// []byte values are converted to string and bounded the same way; nested
// maps are recursed into. All other types (numbers, bools, short strings,
// slices of non-strings) pass through unchanged.
//
// Use this when you need to log a generic "details" / "metadata" bag
// without trusting that every tool / handler keeps its values short. It
// guarantees that a single oversized field (e.g. final_result, raw body,
// stack trace) cannot blow up a log line. See docs/logging.md.
func BoundedDetails(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = boundValue(v)
	}
	return out
}

func boundValue(v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		if len(t) > boundedDetailsThreshold {
			return PreviewHeadTail(t, 15, 10)
		}
		return t
	case []byte:
		if len(t) > boundedDetailsThreshold {
			return PreviewHeadTail(string(t), 15, 10)
		}
		return string(t)
	case map[string]interface{}:
		return BoundedDetails(t)
	case []interface{}:
		// Bound each element but keep the slice shape so structured
		// fields stay queryable in Loki.
		bounded := make([]interface{}, len(t))
		for i, el := range t {
			bounded[i] = boundValue(el)
		}
		return bounded
	default:
		return v
	}
}

// PreviewHeadTail builds a debug-safe head+tail preview suitable for long
// conversation messages — typically content_preview (user → agent) and
// result_preview (agent → user).
//
// Format: "<first headWords words> [..N chars..] <last tailWords words>",
// where N is the number of characters omitted from the middle. When the
// string is short enough that head+tail would already cover the whole value,
// the original (whitespace-collapsed) string is returned unchanged.
//
// The motivation is debugger UX: a user might paste a long document and put
// the actual question on the last line; a beginning-only preview would hide
// it. Head+tail also makes the same message recognisable across io,
// orchestrator, and agent logs.
func PreviewHeadTail(s string, headWords, tailWords int) string {
	if headWords <= 0 {
		headWords = 15
	}
	if tailWords <= 0 {
		tailWords = 10
	}
	flat := strings.Join(strings.Fields(s), " ")
	if flat == "" {
		return ""
	}
	words := strings.Split(flat, " ")
	if len(words) <= headWords+tailWords {
		return flat
	}
	head := strings.Join(words[:headWords], " ")
	tail := strings.Join(words[len(words)-tailWords:], " ")
	omitted := utf8.RuneCountInString(flat) - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	if omitted <= 0 {
		return flat
	}
	return head + " [.." + strconv.Itoa(omitted) + " chars..] " + tail
}
