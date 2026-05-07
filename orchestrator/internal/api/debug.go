// Package api exposes debug HTTP endpoints for the orchestrator.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// DebugHandler serves the /debug/trace/{trace_id} endpoint.
type DebugHandler struct {
	LokiURL string // e.g. "http://loki:3100"
}

// TraceLogEntry is one structured log line returned by the endpoint.
type TraceLogEntry struct {
	Timestamp string         `json:"timestamp"`
	Level     string         `json:"level"`
	Module    string         `json:"module,omitempty"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// GetTrace handles GET /debug/trace/{trace_id}.
// Queries Loki for all logs with the given trace_id in the last hour and
// returns them as a chronological JSON timeline.
func (h *DebugHandler) GetTrace(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("trace_id")
	if traceID == "" {
		http.Error(w, "trace_id required", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf(`{component="orchestrator"} | json | trace_id="%s"`, traceID)
	start := time.Now().Add(-1 * time.Hour).UnixNano()
	lokiURL := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&limit=1000&direction=forward",
		h.LokiURL,
		url.QueryEscape(query),
		start,
	)

	resp, err := http.Get(lokiURL) //nolint:noctx // debug endpoint; context not critical
	if err != nil {
		http.Error(w, fmt.Sprintf("loki query failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read loki response: %v", err), http.StatusBadGateway)
		return
	}

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("loki returned %d: %s", resp.StatusCode, string(body)), http.StatusBadGateway)
		return
	}

	timeline := parseLokiResponse(body)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"trace_id": traceID,
		"entries":  timeline,
		"count":    len(timeline),
	})
}

// lokiQueryRangeResult is the subset of the Loki query_range response we need.
type lokiQueryRangeResult struct {
	Data struct {
		Result []struct {
			Values [][2]string `json:"values"` // [nanosecond-timestamp, log-line]
		} `json:"result"`
	} `json:"data"`
}

// parseLokiResponse flattens Loki streams into a chronological []TraceLogEntry.
func parseLokiResponse(body []byte) []TraceLogEntry {
	var result lokiQueryRangeResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	type tsEntry struct {
		ts    int64
		entry TraceLogEntry
	}
	var all []tsEntry

	for _, stream := range result.Data.Result {
		for _, pair := range stream.Values {
			var ns int64
			fmt.Sscanf(pair[0], "%d", &ns)

			entry := parseLogLine(pair[1])
			all = append(all, tsEntry{ts: ns, entry: entry})
		}
	}

	sort.Slice(all, func(i, j int) bool { return all[i].ts < all[j].ts })

	entries := make([]TraceLogEntry, len(all))
	for i, a := range all {
		entries[i] = a.entry
	}
	return entries
}

// parseLogLine parses a JSON log line into a TraceLogEntry.
// Non-JSON lines are returned with just a message field.
func parseLogLine(line string) TraceLogEntry {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return TraceLogEntry{Message: line}
	}

	entry := TraceLogEntry{
		Fields: make(map[string]any),
	}

	// Extract well-known fields; everything else goes into Fields.
	skip := map[string]bool{
		"time": true, "level": true, "msg": true,
		"module": true, "component": true, "node_id": true,
		"trace_id": true, "task_id": true,
	}
	for k, v := range raw {
		switch k {
		case "time":
			if s, ok := v.(string); ok {
				entry.Timestamp = s
			}
		case "level":
			if s, ok := v.(string); ok {
				entry.Level = s
			}
		case "msg":
			if s, ok := v.(string); ok {
				entry.Message = s
			}
		case "module":
			if s, ok := v.(string); ok {
				entry.Module = s
			}
		default:
			if !skip[k] {
				entry.Fields[k] = v
			}
		}
	}
	if len(entry.Fields) == 0 {
		entry.Fields = nil
	}
	return entry
}
