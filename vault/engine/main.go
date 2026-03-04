package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	engine "github.com/mlim3/cerberOS/vault/engine/vm"
)

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

	var vm engine.VM = engine.NewQEMU(cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Println("starting vm...")
	if err := vm.Start(ctx); err != nil {
		log.Fatalf("failed to start vm: %v", err)
	}

	<-ctx.Done()
	fmt.Println("shutting down...")
	if err := vm.Stop(); err != nil {
		log.Printf("error stopping vm: %v", err)
	}
}
