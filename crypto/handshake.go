package crypto

import "strings"

// Features describe verified protocol behavior, not a software-version allowlist.
const SecurityFeatures = "hkdf-extract-v2,response-auth-v1,sse-sequence-v1,rejection-auth-v1"

type Handshake struct {
	PublicKey         string `json:"public_key"`
	Nonce             string `json:"nonce"`
	Signature         string `json:"signature"`
	Features          string `json:"features"`
	Challenge         string `json:"challenge"`
	FeaturesSignature string `json:"features_signature"`
}

func NewHandshake(apiKey []byte, pair *KeyPair, challenge string) (Handshake, error) {
	nonce, err := GenerateNonce()
	if err != nil {
		return Handshake{}, err
	}
	return SignHandshake(apiKey, pair.PublicKeyBase64(), nonce, challenge), nil
}

func SignHandshake(apiKey []byte, publicKey, nonce, challenge string) Handshake {
	document := Handshake{PublicKey: publicKey, Nonce: nonce, Challenge: challenge, Features: SecurityFeatures}
	// The existing proof remains readable by old clients during a server-first update.
	document.Signature = SignPayload(apiKey, nonce, publicKey)
	document.FeaturesSignature = SignPayload(apiKey, nonce, document.securityPayload())
	return document
}

func (h Handshake) securityPayload() string {
	return "conch-handshake-v2|" + h.PublicKey + "|" + h.Challenge + "|" + h.Features
}

func VerifyHandshakeSecurity(apiKey []byte, h Handshake, challenge string) bool {
	if challenge == "" || h.Challenge != challenge || h.Nonce == "" ||
		!VerifyPayload(apiKey, h.Nonce, h.PublicKey, h.Signature) ||
		!VerifyPayload(apiKey, h.Nonce, h.securityPayload(), h.FeaturesSignature) {
		return false
	}
	features := make(map[string]bool)
	for _, feature := range strings.Split(h.Features, ",") {
		features[feature] = true
	}
	for _, required := range strings.Split(SecurityFeatures, ",") {
		if !features[required] {
			return false
		}
	}
	return true
}
