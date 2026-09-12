package crypto

import (
	"strings"
	"testing"
)

func TestHandshakeAuthenticatesCapabilitiesAndFreshChallenge(t *testing.T) {
	key := []byte("isolated-handshake-authority")
	pair, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	document := SignHandshake(key, pair.PublicKeyBase64(), "server-nonce", "client-challenge")
	for _, change := range []string{"none", "public key", "nonce", "challenge", "proof", "stripped", "signed missing", "future"} {
		t.Run(change, func(t *testing.T) {
			candidate := document
			switch change {
			case "public key":
				candidate.PublicKey = "substituted"
			case "nonce":
				candidate.Nonce = "replayed"
			case "challenge":
				candidate.Challenge = "previous-challenge"
			case "proof":
				candidate.FeaturesSignature = strings.Repeat("0", 64)
			case "stripped":
				candidate.Features = ""
			case "signed missing":
				candidate.Features = "hkdf-extract-v2"
				candidate.FeaturesSignature = SignPayload(key, candidate.Nonce, candidate.securityPayload())
			case "future":
				candidate.Features += ",future-capability"
				candidate.FeaturesSignature = SignPayload(key, candidate.Nonce, candidate.securityPayload())
			}
			want := change == "none" || change == "future"
			if VerifyHandshakeSecurity(key, candidate, "client-challenge") != want {
				t.Fatal("incorrect capability authentication")
			}
		})
	}
	if VerifyHandshakeSecurity(key, document, "") || VerifyHandshakeSecurity([]byte("wrong-key"), document, "client-challenge") {
		t.Fatal("unauthenticated handshake accepted")
	}
}
