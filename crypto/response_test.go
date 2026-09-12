package crypto

import "testing"

func TestResponseAuthenticationVectorAndMutation(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	body := []byte("opaque-response-body")
	const expected = "c07386ab0ed3de798935fbf4a2da6786248563454677fc28721d16d30bcd317e"
	if got := ResponseSignature(key, 200, body); got != expected {
		t.Fatalf("cross-language vector: %s", got)
	}
	if !VerifyResponseSignature(key, 200, body, expected) {
		t.Fatal("valid response rejected")
	}
	for name, valid := range map[string]bool{
		"missing":           VerifyResponseSignature(key, 200, body, ""),
		"status":            VerifyResponseSignature(key, 201, body, expected),
		"body":              VerifyResponseSignature(key, 200, []byte("other-body"), expected),
		"another request":   VerifyResponseSignature([]byte("abcdef0123456789abcdef0123456789"), 200, body, expected),
		"request signature": VerifyResponseSignature(key, 200, body, Sign(key, "1", "POST", "/file/read", SHA256Hex(body), "nonce", "public-key")),
	} {
		if valid {
			t.Errorf("accepted %s", name)
		}
	}
}
