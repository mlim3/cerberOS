package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/mlim3/cerberOS/vault/engine/initrd"
	"github.com/mlim3/cerberOS/vault/engine/orchestrator"
	"github.com/mlim3/cerberOS/vault/engine/preprocessor"
	"github.com/mlim3/cerberOS/vault/engine/secretclient"
	engine "github.com/mlim3/cerberOS/vault/engine/vm"
)

type controller struct {
	mu      sync.Mutex
	vm      engine.VM
	orch    *orchestrator.Orchestrator
	cancel  context.CancelFunc
	running bool
}

func (c *controller) handleStart(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		http.Error(w, "vm already running", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	if err := c.vm.Start(ctx); err != nil {
		cancel()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c.running = true
	w.WriteHeader(http.StatusOK)
}

func (c *controller) handleStop(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		http.Error(w, "vm not running", http.StatusConflict)
		return
	}
	c.cancel()
	if err := c.vm.Stop(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c.running = false
	w.WriteHeader(http.StatusOK)
}

func (c *controller) stopVM() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return
	}
	c.cancel()
	_ = c.vm.Stop()
	c.running = false
}

type executeRequest struct {
	Script string `json:"script"`
}

func (c *controller) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req executeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	resp, err := c.orch.Execute(r.Context(), orchestrator.Request{
		Script: []byte(req.Script),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func main() {
	cfg := engine.DefaultQEMUConfig()

	if v := os.Getenv("KERNEL_IMAGE_PATH"); v != "" {
		cfg.KernelImagePath = v
	}
	if v := os.Getenv("INITRD_PATH"); v != "" {
		cfg.InitrdPath = v
	}
	if v := os.Getenv("QEMU_ACCEL"); v != "" {
		cfg.Accel = v
	}

	secretStoreURL := os.Getenv("SECRET_STORE_URL")
	if secretStoreURL == "" {
		secretStoreURL = "http://localhost:8001"
	}
	secretStoreToken := os.Getenv("SECRET_STORE_TOKEN")
	if secretStoreToken == "" {
		log.Fatal("SECRET_STORE_TOKEN env var is required")
	}

	store := secretclient.New(secretStoreURL, secretStoreToken)
	pp := preprocessor.New(store)
	builder := initrd.New(cfg.InitrdPath)
	orch := orchestrator.New(pp, builder, cfg)

	ctrl := &controller{
		vm:   engine.NewQEMU(cfg),
		orch: orch,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/start", ctrl.handleStart)
	mux.HandleFunc("/stop", ctrl.handleStop)
	mux.HandleFunc("/execute", ctrl.handleExecute)

	srv := &http.Server{Addr: ":8000", Handler: mux}

	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		<-sigCtx.Done()
		ctrl.stopVM()
		_ = srv.Shutdown(context.Background())
	}()

	log.Println("listening on :8000")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
