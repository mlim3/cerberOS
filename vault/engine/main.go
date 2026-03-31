package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mlim3/cerberOS/vault/engine/audit"
	"github.com/mlim3/cerberOS/vault/engine/handlers"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretmanager"
)

func main() {
	auditor := audit.New(audit.NewJSONExporter(os.Stdout))

	manager := secretmanager.NewOpenBaoSecretManager(auditor)
	pp := preprocessor.New(manager, auditor)

	h := handlers.New(pp, auditor, manager)

	mux := http.NewServeMux()
	mux.HandleFunc("/inject", h.Inject)
	mux.HandleFunc("/secrets/get", h.SecretGet)
	mux.HandleFunc("/secrets/put", h.SecretPut)
	mux.HandleFunc("/secrets/delete", h.SecretDelete)

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
