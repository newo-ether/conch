package mcp

import (
	"net/http"

	"github.com/newo-ether/conch/crypto"
)

func authenticatedHTTPError(apiKey []byte, request *http.Request, response *http.Response, body []byte) error {
	signature := response.Header.Get(crypto.RejectionSignatureHeader)
	requestSignature := request.Header.Get("X-Signature")
	verified := len(apiKey) != 0 && requestSignature != "" && signature != "" &&
		crypto.VerifyPayload(apiKey, requestSignature,
			crypto.RejectionPayload(response.StatusCode, body), signature)
	return &serverHTTPError{
		statusCode: response.StatusCode, body: string(body), authenticatedRejection: verified,
	}
}
