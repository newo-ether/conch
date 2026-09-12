package handler

import (
	"github.com/newo-ether/conch/crypto"
	"github.com/newo-ether/conch/encryptedhttp"
	"net/http"
)

const MaxRequestBodyBytes = encryptedhttp.MaxRequestBodyBytes
const maxAuthenticatingRequests = encryptedhttp.MaxAuthenticatingRequests

func AuthMiddleware(apiKey []byte, tracker *crypto.NonceTracker) func(http.Handler) http.Handler {
	return encryptedhttp.AuthMiddleware(apiKey, tracker)
}
