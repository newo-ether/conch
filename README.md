# conch

A lightweight, zero-dependency shell execution server with SSE streaming and end-to-end encryption. Expose a shell on any device — Linux server, Termux (Android), Windows — and control it securely over the network. Ships with a companion MCP binary for Claude Desktop integration.

## Features

- **End-to-end encryption** — X25519 ECDH key exchange + AES-256-GCM, authenticated by a pre-shared API key
- **HMAC request signing** — proves API key possession without transmitting it
- **Anti-replay protection** — time-bucketed nonce tracking with ±5 min clock skew tolerance
- **SSE streaming** — stdout and stderr delivered line-by-line
- **Rate limiting** — per-IP token bucket (20 req/s sustained, burst 40)
- **Single binary** — zero dependencies, statically linked, ~8 MiB
- **Cross-platform** — Linux (arm64/amd64), Windows (amd64), Termux on Android
- **MCP support** — separate `conch-mcp` binary bridges Claude Desktop (stdio/JSON-RPC) to a remote Conch server

## Installation

Download from [releases](https://github.com/newo-ether/conch/releases) or build from source:

```sh
git clone git@github.com:newo-ether/conch.git
cd conch

# Server binary
make all              # all platforms
make build-linux-arm64
make build-linux-amd64
make build-windows-amd64

# MCP binary (Claude Desktop integration)
make mcp-all
make mcp-linux-arm64
make mcp-linux-amd64
make mcp-windows-amd64
```

Binaries land in `build/`.

## Quick start

```sh
export CONCH_API_KEY="your-secret-key"
./conch-linux-amd64
# Server public key: <base64url-encoded>
# conch listening on 0.0.0.0:14216
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `CONCH_HOST` | `0.0.0.0` | Listen address |
| `CONCH_PORT` | `14216` | HTTP listen port |
| `CONCH_API_KEY` | _(required)_ | Pre-shared key for HMAC signing and key-exchange verification. Server refuses to start without it unless `CONCH_ALLOW_NO_AUTH=true`. |
| `CONCH_ALLOW_NO_AUTH` | `false` | Start without an API key. Encryption and signing are disabled; all requests are accepted in plaintext. |
| `CONCH_TIMEOUT` | `30` | Default command timeout in seconds |
| `CONCH_MAX_TIMEOUT` | `120` | Maximum allowed timeout in seconds |

## Protocol

### `GET /health`

No auth. Returns `{"status":"ok"}`.

### `GET /public-key`

Returns the server's persistent X25519 public key, signed with HMAC-SHA256(key, nonce|public_key) to prevent MITM substitution:

```json
{
  "public_key": "<base64url 32B>",
  "nonce": "<base64url 12B>",
  "signature": "<64 hex chars>"
}
```

Clients verify this signature before trusting the key.

### `POST /execute`

Execute a shell command. Every request carries an HMAC signature that proves API key possession.

**Headers:**

| Header | Required | Description |
|--------|----------|-------------|
| `X-Signature` | yes | `HMAC-SHA256(key, timestamp\|method\|path\|bodySHA256\|nonce\|clientPubKey)` → 64 hex |
| `X-Timestamp` | yes | Unix epoch seconds |
| `X-Nonce` | yes | Random 12-byte base64url nonce |
| `X-Encryption` | yes | Must be `v1` |
| `X-Client-Public-Key` | yes | Ephemeral X25519 public key (base64url 32B) |
| `Content-Type` | yes | `application/octet-stream` |

**Request body:** Encrypted with AES-256-GCM using a key derived via ECDH(client_ephemeral_priv, server_pub) → HKDF-SHA256.

After decryption, the body is JSON:

```json
{
  "command": "ls -la /tmp",
  "timeout_ms": 10000,
  "workdir": "/tmp"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `command` | string | yes | Shell command to execute |
| `timeout_ms` | integer | no | Per-command timeout in milliseconds (capped at `CONCH_MAX_TIMEOUT`) |
| `workdir` | string | no | Working directory for the command |

**Response:** `text/event-stream` with encrypted data payloads (each `data:` line is base64url-encoded AES-256-GCM ciphertext):

```
event: line
data: <base64url ciphertext>

event: result
data: <base64url ciphertext>
```

**Error responses:** `401` for all auth failures (signature mismatch, clock skew, replayed nonce, missing headers). `400` for decryption errors. `405` for wrong method.

### Protocol summary

```
Client                           Server
  |                                 |
  |---- GET /public-key ----------->|  (verify HMAC signature)
  |<--- {pubKey, nonce, sig} ------|
  |                                 |
  |  ECDH(eph_priv, server_pub) -> AES key
  |  Encrypt command JSON
  |  HMAC sign all request fields
  |                                 |
  |---- POST /execute ------------->|  (verify HMAC, check nonce, decrypt)
  |<--- SSE stream (encrypted) ----|
```

## MCP (Claude Desktop integration)

The `conch-mcp` binary exposes a `shell_execute` tool to Claude Desktop via the Model Context Protocol.

### Multi-device configuration

```json
{
  "mcpServers": {
    "conch": {
      "command": "/path/to/conch-mcp-windows-amd64.exe",
      "env": {
        "CONCH_DEVICES": "{\"pi\":{\"url\":\"http://192.168.1.100:14216\",\"key\":\"xxx\",\"description\":\"Raspberry Pi 4\"},\"laptop\":{\"url\":\"http://192.168.1.200:14216\",\"key\":\"yyy\",\"description\":\"Windows laptop\"}}"
      }
    }
  }
}
```

### Single-device (legacy)

```json
{
  "mcpServers": {
    "conch": {
      "command": "/path/to/conch-mcp-windows-amd64.exe",
      "env": {
        "CONCH_SERVER_URL": "http://<host>:14216",
        "CONCH_API_KEY": "<your-api-key>"
      }
    }
  }
}
```

### Tool schema

**`shell_execute`** — Execute a command on the remote Conch server.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `device` | string | yes* | — | Target device name (required when managing multiple devices) |
| `command` | string | yes | — | Shell command to execute |
| `timeout_ms` | integer | no | 30000 | Timeout in milliseconds (max 120000) |
| `workdir` | string | no | — | Working directory |

## Cross-compilation

```sh
make build-linux-arm64    # Termux on Android, Raspberry Pi
make build-linux-amd64    # Linux servers
make build-windows-amd64  # Windows
make mcp-linux-arm64      # MCP binary
make mcp-linux-amd64
make mcp-windows-amd64
make all                  # All server targets
make mcp-all              # All MCP targets
```

## License

MIT — see [LICENSE](LICENSE).
