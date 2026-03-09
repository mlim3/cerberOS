package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// TLSConfigFromEnv returns TLS config when AEGIS_NATS_TLS_CA is set (NFR-DB-006).
func TLSConfigFromEnv() (*tls.Config, error) {
	caPath := os.Getenv("AEGIS_NATS_TLS_CA")
	if caPath == "" {
		return nil, nil
	}
	caPem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read TLS CA: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPem)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
	}, nil
}

// NewSecureConnection connects to NATS with NKey auth and TLS 1.3.
// nkeyPath is the path to the NKey seed file (e.g. SU... base64).
func NewSecureConnection(serverURL, nkeyPath string) (*nats.Conn, error) {
	seed, err := os.ReadFile(nkeyPath)
	if err != nil {
		return nil, fmt.Errorf("read nkey seed: %w", err)
	}
	seedStr := string(seed)

	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("parse nkey: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	sign := func(nonce []byte) ([]byte, error) {
		return kp.Sign(nonce)
	}

	opts := []nats.Option{
		nats.Nkey(pub, sign),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
	}
	if strings.HasPrefix(serverURL, "nats+tls") || strings.HasPrefix(serverURL, "tls://") {
		opts = append(opts, nats.Secure(&tls.Config{MinVersion: tls.VersionTLS13}))
	}
	nc, err := nats.Connect(serverURL, opts...)
	if err != nil {
		return nil, err
	}
	_ = seedStr
	return nc, nil
}

// GenerateUserNKey creates a User NKey (U prefix) for NATS client auth.
// Returns (publicKey, seed, error). Seed must be kept secret.
func GenerateUserNKey() (publicKey string, seed []byte, err error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return "", nil, fmt.Errorf("create user nkey: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", nil, err
	}
	seed, err = kp.Seed()
	if err != nil {
		return "", nil, err
	}
	return pub, seed, nil
}

// GenerateNKey creates a new Ed25519 NKey pair for the component.
// Writes the public key to registryPath, returns the seed via the named
// env var (caller must export it; seed is never written to disk).
func GenerateNKey(componentName, registryPath string) (envVarName string, err error) {
	kp, err := nkeys.CreateAccount()
	if err != nil {
		return "", fmt.Errorf("create nkey: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", err
	}
	seed, err := kp.Seed()
	if err != nil {
		return "", err
	}

	// Append public key to registry
	line := fmt.Sprintf("%s=%s\n", componentName, pub)
	f, err := os.OpenFile(registryPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("open registry: %w", err)
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

	envVarName = "AEGIS_NKEY_SEED_" + componentName
	fmt.Fprintf(os.Stderr, "Generated NKey for %s. Export seed to %s (never commit):\n  export %s=%q\n",
		componentName, envVarName, envVarName, string(seed))
	return envVarName, nil
}

// NewConnectionWithNKeySeed connects using the given NKey seed (e.g. from OpenBao).
// Uses TLS when serverURL is tls:// and AEGIS_NATS_TLS_CA is set.
func NewConnectionWithNKeySeed(serverURL, seed string) (*nats.Conn, error) {
	return NewConnectionWithNKeySeedAndName(serverURL, seed, "")
}

// NewConnectionWithNKeySeedAndName connects with NKey and sets the connection name for connz/Grafana.
func NewConnectionWithNKeySeedAndName(serverURL, seed, connName string) (*nats.Conn, error) {
	if seed == "" {
		return nil, fmt.Errorf("nkey seed is empty")
	}
	kp, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return nil, fmt.Errorf("parse nkey: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	sign := func(nonce []byte) ([]byte, error) {
		return kp.Sign(nonce)
	}
	opts := []nats.Option{
		nats.Nkey(pub, sign),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500 * time.Millisecond),
	}
	if connName != "" {
		opts = append(opts, nats.Name(connName))
	}
	if cfg, _ := TLSConfigFromEnv(); cfg != nil {
		opts = append(opts, nats.Secure(cfg))
	}
	return nats.Connect(serverURL, opts...)
}

// NewConnectionWithEnvSeed connects using seed from env var (e.g. AEGIS_NKEY_SEED).
// For demo/stub use when seed is in env, not on disk.
func NewConnectionWithEnvSeed(serverURL, seedEnvVar string) (*nats.Conn, error) {
	seed := os.Getenv(seedEnvVar)
	if seed == "" {
		return nil, fmt.Errorf("%s not set", seedEnvVar)
	}
	return NewConnectionWithNKeySeed(serverURL, seed)
}

// EnsureRegistryDir creates the directory for the NKey registry if needed.
func EnsureRegistryDir(registryPath string) error {
	dir := filepath.Dir(registryPath)
	return os.MkdirAll(dir, 0700)
}
