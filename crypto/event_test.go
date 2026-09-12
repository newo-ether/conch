package crypto

import (
	"strings"
	"testing"
)

func TestEncryptedEventAuthenticatesSequenceAndKind(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	encoded, err := EncryptEvent(key, "line", 7, []byte(`{"line":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := DecryptEvent(key, "line", 7, encoded)
	if err != nil || string(actual) != `{"line":"hello"}` {
		t.Fatalf("roundtrip: %s, %v", actual, err)
	}
	for _, index := range []uint64{0, 6, 8} {
		if _, err := DecryptEvent(key, "line", index, encoded); err == nil {
			t.Fatalf("accepted misplaced frame %d", index)
		}
	}
	if _, err := DecryptEvent(key, "result", 7, encoded); err == nil {
		t.Fatal("accepted renamed event")
	}
	other := []byte("abcdef0123456789abcdef0123456789")
	if _, err := DecryptEvent(other, "line", 7, encoded); err == nil {
		t.Fatal("accepted another stream key")
	}
}

func TestEncryptedEventRejectsLegacyPayloadAndNullSequence(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, payload := range []string{
		`{"line":"legacy"}`,
		`{"_conch_event":"line","_conch_sequence":null}`,
		`{"_conch_event":"line","_conch_sequence":-1}`,
		`{"_conch_event":"line","_conch_sequence":0.0}`,
	} {
		encoded, err := Encrypt(key, []byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecryptEvent(key, "line", 0, encoded); err == nil {
			t.Fatal("accepted invalid frame")
		}
	}
	encoded, err := EncryptEvent(key, "line", 0, []byte(`{"text":"present"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "present") {
		t.Fatal("plaintext visible on wire")
	}
}
