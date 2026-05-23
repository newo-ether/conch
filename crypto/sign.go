package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Sign creates an HMAC-SHA256 signature over the concatenated fields.
// Returns hex-encoded signature (64 chars).
func Sign(apiKey []byte, timestamp, method, path, bodySHA256, nonce, clientPubKey string) string {
	message := fmt.Sprintf("%s|%s|%s|%s|%s|%s", timestamp, method, path, bodySHA256, nonce, clientPubKey)
	mac := hmac.New(sha256.New, apiKey)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignPayload creates an HMAC-SHA256 signature over nonce|payload.
func SignPayload(apiKey []byte, nonce, payload string) string {
	message := fmt.Sprintf("%s|%s", nonce, payload)
	mac := hmac.New(sha256.New, apiKey)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks an HMAC-SHA256 signature in constant time.
func Verify(apiKey []byte, timestamp, method, path, bodySHA256, nonce, clientPubKey, signatureHex string) bool {
	expected := Sign(apiKey, timestamp, method, path, bodySHA256, nonce, clientPubKey)
	return hmac.Equal([]byte(expected), []byte(signatureHex))
}

// VerifyPayload checks a payload signature in constant time.
func VerifyPayload(apiKey []byte, nonce, payload, signatureHex string) bool {
	expected := SignPayload(apiKey, nonce, payload)
	return hmac.Equal([]byte(expected), []byte(signatureHex))
}

// SHA256Hex returns the hex-encoded SHA-256 hash of data.
func SHA256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
