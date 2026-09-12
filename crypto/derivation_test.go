package crypto

import (
	"crypto/ecdh"
	"encoding/hex"
	"testing"
)

func TestV2X25519HKDFMatchesAndroidAndIndependentHMACVector(t *testing.T) {
	decode := func(value string) []byte {
		data, err := hex.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	// RFC 7748 Alice private / Bob public inputs. The v2 HKDF result was also
	// calculated using .NET HMAC-SHA256, separately from both client libraries.
	private, err := ecdh.X25519().NewPrivateKey(decode("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a"))
	if err != nil {
		t.Fatal(err)
	}
	public, err := ecdh.X25519().NewPublicKey(decode("de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := DeriveSharedSecret(private, public)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "d551f7913184eaeb096dd8fe635ebf6017d7ca3159f721aa2fa43f54e4edc4d8"
	if hex.EncodeToString(result) != expected {
		t.Fatalf("v2 key = %x", result)
	}
	legacy, err := DeriveLegacySharedSecret(private, public)
	if err != nil || hex.EncodeToString(legacy) == expected {
		t.Fatal("v2 collapsed into legacy derivation", err)
	}
	lowOrder, _ := ecdh.X25519().NewPublicKey(make([]byte, 32))
	if _, err := DeriveSharedSecret(private, lowOrder); err == nil {
		t.Fatal("low-order public key accepted")
	}
}
