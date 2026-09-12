package crypto

import (
	"encoding/json"
	"fmt"
)

const eventKindField = "_conch_event"
const eventSequenceField = "_conch_sequence"

// EncryptEvent authenticates ordering and event interpretation inside the
// existing AES-GCM payload. Sequence starts at zero for each request's fresh key.
// Ordinary payload fields retain their original JSON values.
func EncryptEvent(key []byte, kind string, sequence uint64, payload []byte) (string, error) {
	if kind == "" {
		return "", fmt.Errorf("missing encrypted event kind")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return "", fmt.Errorf("encrypted event payload must be an object")
	}
	fields[eventKindField], _ = json.Marshal(kind)
	fields[eventSequenceField], _ = json.Marshal(sequence)
	framed, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return Encrypt(key, framed)
}

// DecryptEvent rejects replay, removal, reordering and outer SSE event renaming.
// Stream readers must also require their protocol's authentic terminal event;
// EOF alone is never completion. Legacy unframed events fail closed.
func DecryptEvent(key []byte, kind string, sequence uint64, encoded string) ([]byte, error) {
	payload, err := Decrypt(key, encoded)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("encrypted event payload must be an object")
	}
	var actualKind string
	var actualSequence *uint64
	if err := json.Unmarshal(fields[eventKindField], &actualKind); err != nil || actualKind != kind {
		return nil, fmt.Errorf("encrypted event kind mismatch")
	}
	if err := json.Unmarshal(fields[eventSequenceField], &actualSequence); err != nil ||
		actualSequence == nil || *actualSequence != sequence {
		return nil, fmt.Errorf("encrypted event sequence mismatch")
	}
	delete(fields, eventKindField)
	delete(fields, eventSequenceField)
	return json.Marshal(fields)
}
