package logic

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	ErrMasterKeyNotSet = errors.New("VAULT_MASTER_KEY environment variable is not set")
	ErrInvalidKeySize  = errors.New("VAULT_MASTER_KEY must be exactly 32 bytes for AES-256")
)

// VaultManager handles encryption and decryption of secrets using AES-256-GCM.
type VaultManager struct {
	masterKey []byte
	aead      cipher.AEAD
}

// NewVaultManager initializes a new VaultManager.
// It loads the master key from the VAULT_MASTER_KEY environment variable.
// If the variable is not set or the key size is invalid, it returns an error.
func NewVaultManager() (*VaultManager, error) {
	keyHex := os.Getenv("VAULT_MASTER_KEY")
	if keyHex == "" {
		return nil, ErrMasterKeyNotSet
	}

	key := []byte(keyHex)
	if len(key) != 32 {
		return nil, ErrInvalidKeySize
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher block: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	return &VaultManager{
		masterKey: key,
		aead:      aead,
	}, nil
}

// Encrypt encrypts a plaintext string and returns the ciphertext and nonce.
func (v *VaultManager) Encrypt(plaintext string) (ciphertext []byte, nonce []byte, err error) {
	nonce = make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext = v.aead.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, nonce, nil
}

// Decrypt decrypts a ciphertext using the provided nonce and returns the plaintext string.
func (v *VaultManager) Decrypt(ciphertext []byte, nonce []byte) (string, error) {
	if len(nonce) != v.aead.NonceSize() {
		return "", errors.New("invalid nonce size")
	}

	plaintext, err := v.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}
