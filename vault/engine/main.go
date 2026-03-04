package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	engine "github.com/mlim3/cerberOS/vault/engine/vm"
)

type controller struct {
	mu      sync.Mutex
	vm      engine.VM
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

	ctrl := &controller{vm: engine.NewQEMU(cfg)}

	mux := http.NewServeMux()
	mux.HandleFunc("/start", ctrl.handleStart)
	mux.HandleFunc("/stop", ctrl.handleStop)

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
