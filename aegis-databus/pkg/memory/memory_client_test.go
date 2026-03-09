package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// exerciseMemoryClient runs the same operations against any MemoryClient.
// Both MockMemoryClient and HTTPClient (via fake server) must pass.
func exerciseMemoryClient(ctx context.Context, t *testing.T, client MemoryClient) {
	t.Helper()

	// Ping
	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
		return
	}

	// Stream config
	cfg := StreamConfig{
		Name:     "TEST_STREAM",
		Subjects: []string{"aegis.test.>"},
		MaxAge:   7 * 24 * time.Hour,
		MaxBytes: 10 << 30,
		Replicas: 1,
	}
	if err := client.UpsertStreamConfig(ctx, cfg); err != nil {
		t.Errorf("UpsertStreamConfig: %v", err)
		return
	}
	got, err := client.GetStreamConfig(ctx, "TEST_STREAM")
	if err != nil {
		t.Errorf("GetStreamConfig: %v", err)
		return
	}
	if got.Name != cfg.Name || len(got.Subjects) != 1 || got.Subjects[0] != "aegis.test.>" {
		t.Errorf("GetStreamConfig: got %+v", got)
	}
	list, err := client.ListStreamConfigs(ctx)
	if err != nil || len(list) < 1 {
		t.Errorf("ListStreamConfigs: err=%v len=%d", err, len(list))
	}

	// Consumer state
	if err := client.UpdateConsumerAckSeq(ctx, "S", "C", 42); err != nil {
		t.Errorf("UpdateConsumerAckSeq: %v", err)
	}
	state, err := client.GetConsumerState(ctx, "S", "C")
	if err != nil || state.AckSeq != 42 {
		t.Errorf("GetConsumerState: err=%v state=%+v", err, state)
	}

	// Outbox
	entry := OutboxEntry{
		ID: "o1", Subject: "aegis.test.ev", Payload: []byte(`{"id":"1"}`),
		Status: "pending", AttemptCount: 0, NextRetryAt: time.Now().Add(-time.Second),
	}
	if err := client.InsertOutboxEntry(ctx, entry); err != nil {
		t.Errorf("InsertOutboxEntry: %v", err)
		return
	}
	pending, err := client.FetchPendingOutbox(ctx, 10)
	if err != nil || len(pending) < 1 {
		t.Errorf("FetchPendingOutbox: err=%v len=%d", err, len(pending))
	}
	if err := client.MarkOutboxSent(ctx, "o1", 100); err != nil {
		t.Errorf("MarkOutboxSent: %v", err)
	}

	// Audit log (metadata only, no payload - SR-DB-005)
	audit := AuditLogEntry{
		Subject: "aegis.tasks.routed", Component: "task-router",
		CorrelationID: "corr-1", SizeBytes: 256,
	}
	if err := client.AppendAuditLog(ctx, audit); err != nil {
		t.Errorf("AppendAuditLog: %v", err)
	}
	logs, err := client.ListAuditLogs(ctx, 10)
	if err != nil || len(logs) < 1 {
		t.Errorf("ListAuditLogs: err=%v len=%d", err, len(logs))
	}
	// Audit must not contain payload
	for _, l := range logs {
		if l.Subject == "" || l.Component == "" {
			t.Errorf("audit entry missing metadata: %+v", l)
		}
	}

	// NKey (mock may not have any; that's ok)
	_, _ = client.GetNKey(ctx, "nonexistent") // ErrNotFound expected

	// Cleanup
	if err := client.DeleteStreamConfig(ctx, "TEST_STREAM"); err != nil {
		t.Errorf("DeleteStreamConfig: %v", err)
	}
}

func TestMockMemoryClient_InterfaceContract(t *testing.T) {
	ctx := context.Background()
	client := NewMockMemoryClient()
	exerciseMemoryClient(ctx, t, client)
}

// fakeMemoryServer implements the Memory REST API using a MockMemoryClient.
// Used to test HTTPClient against the same contract as Mock.
func fakeMemoryServer() *httptest.Server {
	backend := NewMockMemoryClient()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/memory/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if backend.Ping(r.Context()) != nil {
			http.Error(w, "ping failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/memory/streams", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		switch r.Method {
		case http.MethodGet:
			list, err := backend.ListStreamConfigs(ctx)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(list)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/memory/streams/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		name := r.URL.Path[len("/v1/memory/streams/"):]
		if name != "" && name[len(name)-1] == '/' {
			name = name[:len(name)-1]
		}
		name, _ = url.PathUnescape(name)
		switch r.Method {
		case http.MethodGet:
			cfg, err := backend.GetStreamConfig(ctx, name)
			if err == ErrNotFound {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cfg)
		case http.MethodPut:
			var cfg StreamConfig
			if json.NewDecoder(r.Body).Decode(&cfg) != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			cfg.Name = name
			if backend.UpsertStreamConfig(ctx, cfg) != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if err := backend.DeleteStreamConfig(ctx, name); err == ErrNotFound {
				http.Error(w, "not found", http.StatusNotFound)
				return
			} else if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/memory/outbox", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var entry OutboxEntry
		if json.NewDecoder(r.Body).Decode(&entry) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if backend.InsertOutboxEntry(r.Context(), entry) != nil {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/v1/memory/outbox/pending", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 10
		if q := r.URL.Query().Get("limit"); q != "" {
			if x, err := strconv.Atoi(q); err == nil && x > 0 {
				limit = x
			}
		}
		list, err := backend.FetchPendingOutbox(r.Context(), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("/v1/memory/outbox/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rest := r.URL.Path[len("/v1/memory/outbox/"):]
		if len(rest) < 5 || rest[len(rest)-5:] != "/sent" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		id := rest[:len(rest)-5]
		id, _ = url.PathUnescape(id)
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var m map[string]uint64
		if json.NewDecoder(r.Body).Decode(&m) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		seq := m["sequence"]
		if err := backend.MarkOutboxSent(ctx, id, seq); err == ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/memory/audit", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if r.Method == http.MethodPost {
			var entry AuditLogEntry
			if json.NewDecoder(r.Body).Decode(&entry) != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if backend.AppendAuditLog(ctx, entry) != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method == http.MethodGet {
			limit := 10
			if q := r.URL.Query().Get("limit"); q != "" {
				if x, err := strconv.Atoi(q); err == nil && x > 0 {
					limit = x
				}
			}
			list, err := backend.ListAuditLogs(ctx, limit)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(list)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/v1/memory/consumers/", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rest := r.URL.Path[len("/v1/memory/consumers/"):]
		var stream, consumer string
		for i, c := range rest {
			if c == '/' {
				stream = rest[:i]
				consumer = rest[i+1:]
				break
			}
		}
		stream, _ = url.PathUnescape(stream)
		consumer, _ = url.PathUnescape(consumer)
		switch r.Method {
		case http.MethodGet:
			state, err := backend.GetConsumerState(ctx, stream, consumer)
			if err == ErrNotFound {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(state)
		case http.MethodPut:
			var m map[string]uint64
			if json.NewDecoder(r.Body).Decode(&m) != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			ackSeq := m["ack_seq"]
			if backend.UpdateConsumerAckSeq(ctx, stream, consumer, ackSeq) != nil {
				http.Error(w, "error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/memory/nkeys/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		component := r.URL.Path[len("/v1/memory/nkeys/"):]
		component, _ = url.PathUnescape(component)
		seed, err := backend.GetNKey(r.Context(), component)
		if err == ErrNotFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"seed": seed})
	})

	return httptest.NewServer(mux)
}

func TestHTTPMemoryClient_InterfaceContract(t *testing.T) {
	server := fakeMemoryServer()
	defer server.Close()

	baseURL := server.URL + "/v1/memory"
	client := NewHTTPClient(baseURL)
	ctx := context.Background()
	exerciseMemoryClient(ctx, t, client)
}
