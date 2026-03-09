// Setup Zero Trust: generates User NKey and writes NATS configs with NKey auth.
// Run: go run ./cmd/setup-zero-trust
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aegis-databus/pkg/security"
)

const placeholder = "__AEGIS_NKEY_PUBLIC__"

func main() {
	pub, seed, err := security.GenerateUserNKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate nkey: %v\n", err)
		os.Exit(1)
	}

	configDir := "config/secure"
	if err := os.MkdirAll(configDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		os.Exit(1)
	}

	for _, name := range []string{"nats-node1.conf", "nats-node2.conf", "nats-node3.conf"} {
		templatePath := filepath.Join(configDir, name)
		content, err := os.ReadFile(templatePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", templatePath, err)
			os.Exit(1)
		}
		newContent := strings.ReplaceAll(string(content), placeholder, pub)
		if err := os.WriteFile(templatePath, []byte(newContent), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", templatePath, err)
			os.Exit(1)
		}
	}

	envPath := ".env.nkeys"
	envLine := fmt.Sprintf("AEGIS_NKEY_SEED=%s\n", string(seed))
	if err := os.WriteFile(envPath, []byte(envLine), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", envPath, err)
		os.Exit(1)
	}

	fmt.Println("Zero Trust setup complete.")
	fmt.Println()
	fmt.Println("1. NATS configs updated in config/secure/ with NKey auth")
	fmt.Printf("2. Seed written to %s (gitignored)\n", envPath)
	fmt.Println()
	fmt.Println("Start with Zero Trust:")
	fmt.Println("  source .env.nkeys")
	fmt.Println("  docker compose -f docker-compose.yml -f docker-compose.secure.yml up -d")
	fmt.Println("  ./bin/aegis-databus &")
	fmt.Println("  ./bin/aegis-demo")
	fmt.Println()
	fmt.Println("Without AEGIS_NKEY_SEED, connections will be rejected.")
}
