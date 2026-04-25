// STUB: Simulates Memory service for DataBus → Orchestrator → Memory demo.
// Exposes Memory API at /v1/memory/*. Uses MockMemoryClient internally.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"aegis-databus/pkg/envelope"
	"aegis-databus/pkg/memory"
)

const defaultAddr = ":8090"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
		With("service", "databus", "component", "memory-stub")

	mock := memory.NewMockMemoryClient()
	ctx := context.Background()
	seedDemo(ctx, mock)

	mux := http.NewServeMux()
	prefix := "/v1/memory"
	mux.HandleFunc(prefix+"/outbox", func(w http.ResponseWriter, r *http.Request) {
		handleOutbox(w, r, mock)
	})
	mux.HandleFunc(prefix+"/outbox/pending", func(w http.ResponseWriter, r *http.Request) {
		handleOutboxPending(w, r, mock)
	})
	mux.HandleFunc(prefix+"/outbox/", func(w http.ResponseWriter, r *http.Request) {
		handleOutboxSent(w, r, mock)
	})
	mux.HandleFunc(prefix+"/audit", func(w http.ResponseWriter, r *http.Request) {
		handleAudit(w, r, mock)
	})
	mux.HandleFunc(prefix+"/ping", func(w http.ResponseWriter, r *http.Request) {
		handlePing(w, r, mock)
	})
	mux.HandleFunc(prefix+"/processed/", func(w http.ResponseWriter, r *http.Request) {
		handleProcessed(w, r, mock, prefix)
	})

	addr := os.Getenv("MEMORY_STUB_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	logger.Info("memory stub listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("memory stub failed", "error", err, "exit_code", 1)
		os.Exit(1)
	}
}

func seedDemo(ctx context.Context, m *memory.MockMemoryClient) {
	ev1 := envelope.Build("aegis/databus-demo", "aegis.tasks.audit_seed", map[string]string{"demo": "audit"})
	ev2 := envelope.Build("aegis/databus-demo", "aegis.memory.audit_seed", map[string]string{"demo": "audit"})
	now := time.Now().UTC()
	subjects := []string{"aegis.tasks.audit_seed", "aegis.memory.audit_seed"}
	payloads := [][]byte{ev1.MustMarshal(), ev2.MustMarshal()}
	for i, subj := range subjects {
		_ = m.InsertOutboxEntry(ctx, memory.OutboxEntry{
			ID:           fmt.Sprintf("audit-demo-%d", i+1),
			Subject:      subj,
			Payload:      payloads[i],
			Status:       "pending",
			AttemptCount: 0,
			NextRetryAt:  now.Add(-time.Second),
			CreatedAt:    now,
		})
	}
}

func handleOutbox(w http.ResponseWriter, r *http.Request, m *memory.MockMemoryClient) {
	if r.Method == http.MethodPost {
		var entry memory.OutboxEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := m.InsertOutboxEntry(r.Context(), entry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func handleOutboxPending(w http.ResponseWriter, r *http.Request, m *memory.MockMemoryClient) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	list, err := m.FetchPendingOutbox(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func handleOutboxSent(w http.ResponseWriter, r *http.Request, m *memory.MockMemoryClient) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/memory/outbox/")
	path = strings.Trim(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[1] != "sent" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	id, _ := url.PathUnescape(parts[0])
	var body struct {
		Sequence uint64 `json:"sequence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := m.MarkOutboxSent(r.Context(), id, body.Sequence); err != nil {
		if err == memory.ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleAudit(w http.ResponseWriter, r *http.Request, m *memory.MockMemoryClient) {
	switch r.Method {
	case http.MethodPost:
		var entry memory.AuditLogEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := m.AppendAuditLog(r.Context(), entry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}
		list, err := m.ListAuditLogs(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handlePing(w http.ResponseWriter, r *http.Request, m *memory.MockMemoryClient) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := m.Ping(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleProcessed implements IdempotencyChecker over HTTP (GET/PUT {prefix}/processed/{id}).
func handleProcessed(w http.ResponseWriter, r *http.Request, m *memory.MockMemoryClient, prefix string) {
	rest := strings.TrimPrefix(r.URL.Path, prefix+"/processed/")
	rest = strings.Trim(rest, "/")
	id, err := url.PathUnescape(rest)
	if err != nil || id == "" {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	var ic memory.IdempotencyChecker = m
	switch r.Method {
	case http.MethodGet:
		ok, err := ic.WasProcessed(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		if err := ic.RecordProcessed(r.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
