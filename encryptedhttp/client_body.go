package encryptedhttp

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/newo-ether/conch/crypto"
)

type responseBody struct {
	outer    io.ReadCloser
	input    *bufio.Reader
	key      []byte
	sequence uint64
	pending  []byte
	ended    bool
}

func newResponseBody(outer io.ReadCloser, key []byte) *responseBody {
	return &responseBody{outer: outer, input: bufio.NewReaderSize(outer, 64<<10), key: key}
}

func (b *responseBody) line() (string, error) {
	raw, err := b.input.ReadSlice('\n')
	if err != nil {
		return "", errors.New("incomplete or oversized encrypted frame")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r"), nil
}

func (b *responseBody) next() (string, []byte, error) {
	event, err := b.line()
	if err != nil {
		return "", nil, err
	}
	data, err := b.line()
	if err != nil {
		return "", nil, err
	}
	blank, err := b.line()
	kind := strings.TrimPrefix(event, "event: ")
	if err != nil || blank != "" || !strings.HasPrefix(event, "event: ") ||
		!strings.HasPrefix(data, "data: ") ||
		(kind != "http_headers" && kind != "http_body" && kind != "http_end") {
		return "", nil, errors.New("invalid encrypted frame")
	}
	decoded, err := crypto.DecryptEvent(b.key, kind, b.sequence, strings.TrimPrefix(data, "data: "))
	if err != nil {
		return "", nil, err
	}
	b.sequence++
	return kind, decoded, nil
}

func (b *responseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 && !b.ended {
		kind, data, err := b.next()
		if err != nil {
			return 0, err
		}
		switch kind {
		case "http_end":
			b.ended = true
		case "http_body":
			var value struct{ Body []byte }
			if json.Unmarshal(data, &value) != nil || len(value.Body) > frameBytes {
				return 0, errors.New("invalid encrypted body bytes")
			}
			b.pending = value.Body
		default:
			return 0, errors.New("unexpected encrypted HTTP headers")
		}
	}
	if len(b.pending) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *responseBody) Close() error { return b.outer.Close() }
