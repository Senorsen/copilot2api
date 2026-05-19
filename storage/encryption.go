package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	pbkdf2Iterations = 100000
	keyLength        = 32 // AES-256
	saltLength       = 16
)

// Encryptor handles encryption/decryption of sensitive data using AES-256-GCM
// with a PBKDF2-derived key from the master key.
type Encryptor struct {
	masterKey []byte
}

// NewEncryptor creates a new Encryptor with the given master key.
func NewEncryptor(masterKey string) *Encryptor {
	return &Encryptor{masterKey: []byte(masterKey)}
}

// Encrypt encrypts plaintext. Output format: base64(salt + nonce + ciphertext).
func (e *Encryptor) Encrypt(plaintext []byte) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	key := pbkdf2.Key(e.masterKey, salt, pbkdf2Iterations, keyLength, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Combine: salt + nonce + ciphertext
	combined := make([]byte, 0, saltLength+len(nonce)+len(ciphertext))
	combined = append(combined, salt...)
	combined = append(combined, nonce...)
	combined = append(combined, ciphertext...)

	return base64.StdEncoding.EncodeToString(combined), nil
}

// Decrypt decrypts a base64-encoded encrypted string.
func (e *Encryptor) Decrypt(encoded string) ([]byte, error) {
	combined, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode: %w", err)
	}

	if len(combined) < saltLength+12 { // minimum: salt + nonce (12 for GCM)
		return nil, fmt.Errorf("ciphertext too short")
	}

	salt := combined[:saltLength]
	key := pbkdf2.Key(e.masterKey, salt, pbkdf2Iterations, keyLength, sha256.New)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(combined) < saltLength+nonceSize {
		return nil, fmt.Errorf("ciphertext too short for nonce")
	}

	nonce := combined[saltLength : saltLength+nonceSize]
	ciphertext := combined[saltLength+nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}
