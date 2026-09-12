package crypto

import "strconv"

const RejectionSignatureHeader = "X-Conch-Rejection-Signature"

// RejectionPayload domain-separates a pre-dispatch response from public-key and
// request signatures. SignPayload's nonce is the exact original request signature,
// which already binds its fresh nonce, timestamp, method, path, body and client key.
func RejectionPayload(status int, response []byte) string {
	return "conch-pre-dispatch-v1|" + strconv.Itoa(status) + "|" + SHA256Hex(response)
}
