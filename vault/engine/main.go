package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

type injectRequest struct {
	Agent  string   `json:"agent"`
	Script string   `json:"script"`
	Keys   []string `json:"keys"` // secret keys the agent is requesting
}

type injectResponse struct {
	Agent  string `json:"agent"`
	Script string `json:"script"` // script with secrets injected
}

type errorResponse struct {
	Error string `json:"error"`
}

type server struct {
	pp      *preprocessor.Preprocessor
	auditor *audit.Logger
}

func (s *server) handleInject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req injectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.auditor.Log(audit.Event{
		Kind:    audit.KindInjection,
		Agent:   req.Agent,
		Keys:    req.Keys,
		Message: "agent requested secret injection",
	})

	// Preprocess: resolve and inject secrets into placeholders.
	// Authorization is atomic — if any key is denied/missing, the entire
	// request fails with no partial injection.
	result, err := s.pp.Process(req.Agent, []byte(req.Script))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(errorResponse{Error: err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(injectResponse{
		Agent:  req.Agent,
		Script: string(result.Script),
	})
}

func main() {
	auditor := audit.New(audit.NewJSONExporter(os.Stdout))

	manager := secretmanager.NewOpenBaoSecretManager(auditor)
	pp := preprocessor.New(manager, auditor)

	srv := &server{pp: pp, auditor: auditor}

	mux := http.NewServeMux()
	mux.HandleFunc("/inject", srv.handleInject)

	httpSrv := &http.Server{Addr: ":8000", Handler: mux}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-sigCtx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	log.Println("vault listening on :8000")
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
