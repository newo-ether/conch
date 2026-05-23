package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const nonceSize = 12 // 96-bit GCM nonce

// Encrypt encrypts plaintext with AES-256-GCM using a random nonce.
// Returns base64url(nonce || ciphertext+tag).
func Encrypt(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	result := make([]byte, 0, nonceSize+len(ciphertext))
	result = append(result, nonce...)
	result = append(result, ciphertext...)
	return B64.EncodeToString(result), nil
}

// Decrypt decrypts a base64url(nonce || ciphertext+tag) payload.
func Decrypt(key []byte, encoded string) ([]byte, error) {
	raw, err := B64.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if len(raw) < nonceSize+1 {
		return nil, fmt.Errorf("ciphertext too short: %d bytes", len(raw))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := raw[:nonceSize]
	ciphertext := raw[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
