package crypto

import (
	"crypto/hmac"
	"fmt"
)

const ResponseSignatureHeader = "X-Conch-Response-Signature"

// ResponseSignature binds the response direction, status and exact ciphertext to
// this request's fresh ECDH-derived session key. A separate HKDF subkey is used
// for HMAC; requests cannot be reflected as authenticated responses.
func ResponseSignature(sessionKey []byte, status int, body []byte) string {
	key := hkdfExpand(sessionKey, []byte("conch-response-auth-v1"), 32)
	payload := fmt.Sprintf("%d|%s", status, SHA256Hex(body))
	return SignPayload(key, "conch-response-v1", payload)
}

func VerifyResponseSignature(sessionKey []byte, status int, body []byte, signature string) bool {
	if len(sessionKey) != 32 || len(signature) != 64 {
		return false
	}
	expected := ResponseSignature(sessionKey, status, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}
