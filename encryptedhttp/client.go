package encryptedhttp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/newo-ether/conch/crypto"
)

// Transport applies the same authenticated channel as the Android client.
// It never follows redirects, downgrades or retries an application request.
type Transport struct {
	Key  []byte
	Base http.RoundTripper
}

func (t *Transport) RoundTrip(original *http.Request) (*http.Response, error) {
	if len(t.Key) == 0 || original.URL.User != nil ||
		(original.URL.Scheme != "http" && original.URL.Scheme != "https") {
		return nil, errors.New("invalid encrypted endpoint")
	}
	var raw []byte
	var err error
	if original.Body != nil {
		defer original.Body.Close()
		raw, err = io.ReadAll(io.LimitReader(original.Body, (512<<10)+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > 512<<10 {
			return nil, errors.New("encrypted request exceeds limit")
		}
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	origin := *original.URL
	origin.Path, origin.RawPath, origin.RawQuery, origin.Fragment = "/public-key", "", "", ""
	challenge, err := crypto.GenerateNonce()
	if err != nil {
		return nil, err
	}
	query := origin.Query()
	query.Set("challenge", challenge)
	origin.RawQuery = query.Encode()
	handshakeRequest, err := http.NewRequestWithContext(original.Context(), "GET", origin.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := base.RoundTrip(handshakeRequest)
	if err != nil {
		return nil, err
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	var handshake crypto.Handshake
	if response.StatusCode != 200 || len(document) > 64<<10 ||
		json.Unmarshal(document, &handshake) != nil ||
		!crypto.VerifyHandshakeSecurity(t.Key, handshake, challenge) {
		return nil, errors.New("encrypted handshake authentication failed")
	}
	pair, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	public, err := crypto.DecodePublicKey(handshake.PublicKey)
	if err != nil {
		return nil, err
	}
	key, err := crypto.DeriveSharedSecret(pair.PrivateKey, public)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(Request{Method: original.Method, Path: original.URL.RequestURI(),
		ContentType: original.Header.Get("Content-Type"), Body: raw})
	if err != nil {
		return nil, err
	}
	encoded, err := crypto.Encrypt(key, plain)
	if err != nil {
		return nil, err
	}
	origin.Path, origin.RawQuery = RequestPath, ""
	request, err := http.NewRequestWithContext(original.Context(), "POST", origin.String(), strings.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.GetBody = nil
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := crypto.GenerateNonce()
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Encryption", "v2")
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Nonce", nonce)
	request.Header.Set("X-Client-Public-Key", pair.PublicKeyBase64())
	request.Header.Set("X-Signature", crypto.Sign(t.Key, timestamp, "POST", RequestPath,
		crypto.SHA256Hex([]byte(encoded)), nonce, pair.PublicKeyBase64()))
	response, err = base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*http.Response, error) { response.Body.Close(); return nil, err }
	if response.StatusCode != 200 {
		return fail(errors.New("encrypted request was not accepted"))
	}
	body := newResponseBody(response.Body, key)
	kind, data, err := body.next()
	if err != nil {
		return fail(err)
	}
	var headers struct {
		Status      int
		ContentType string
	}
	if kind != "http_headers" || json.Unmarshal(data, &headers) != nil ||
		headers.Status < 200 || headers.Status > 599 || len(headers.ContentType) > 256 {
		return fail(errors.New("invalid encrypted HTTP headers"))
	}
	response.StatusCode = headers.Status
	response.Status = strconv.Itoa(headers.Status) + " " + http.StatusText(headers.Status)
	response.Header = make(http.Header)
	response.Header.Set("Content-Type", headers.ContentType)
	response.Header.Set("Cache-Control", "no-store")
	response.Body, response.ContentLength, response.Request = body, -1, original
	response.TransferEncoding = nil
	response.Trailer = nil
	return response, nil
}

// ReadAllBounded is for control responses; images and streams should be read
// incrementally from Response.Body instead of accumulating them in memory.
func ReadAllBounded(body io.Reader, limit int64) ([]byte, error) {
	if limit < 0 || limit > 128<<20 {
		return nil, errors.New("invalid response bound")
	}
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("encrypted response exceeds limit")
	}
	return raw, nil
}
