package crypto

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

var B64 = base64.RawURLEncoding

// KeyPair is an X25519 key pair.
type KeyPair struct {
	PrivateKey *ecdh.PrivateKey
	PublicKey  *ecdh.PublicKey
}

// GenerateKeyPair creates a new X25519 key pair.
func GenerateKeyPair() (*KeyPair, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &KeyPair{PrivateKey: priv, PublicKey: priv.PublicKey()}, nil
}

// PublicKeyBase64 returns the base64url-encoded public key.
func (kp *KeyPair) PublicKeyBase64() string {
	return B64.EncodeToString(kp.PublicKey.Bytes())
}

// DecodePublicKey decodes a base64url-encoded X25519 public key.
func DecodePublicKey(s string) (*ecdh.PublicKey, error) {
	raw, err := B64.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("invalid X25519 public key length: %d", len(raw))
	}
	return ecdh.X25519().NewPublicKey(raw)
}

// DeriveSharedSecret performs ECDH and derives an AES-256 key via HKDF-SHA256.
func DeriveSharedSecret(ourPriv *ecdh.PrivateKey, theirPub *ecdh.PublicKey) ([]byte, error) {
	shared, err := ourPriv.ECDH(theirPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}
	return hkdfExpand(shared, []byte("conch-agora-v1"), 32), nil
}

// hkdfExpand is HKDF-Expand per RFC 5869 (HMAC-SHA256).
func hkdfExpand(prk, info []byte, length int) []byte {
	if length > 255*32 {
		panic("hkdf: requested length too long")
	}
	result := make([]byte, length)
	var t []byte
	for offset, i := 0, byte(1); offset < length; i++ {
		mac := hmac.New(sha256.New, prk)
		if t != nil {
			mac.Write(t)
		}
		mac.Write(info)
		mac.Write([]byte{i})
		t = mac.Sum(nil)
		copyLen := min(len(t), length-offset)
		copy(result[offset:], t[:copyLen])
		offset += copyLen
	}
	return result
}

// GenerateNonce returns a random 12-byte nonce as base64url.
func GenerateNonce() (string, error) {
	b := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return B64.EncodeToString(b), nil
}
