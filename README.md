# conch

A lightweight, zero-dependency shell execution server with SSE streaming. Single binary for Linux, Termux (Android), and Windows.

## Features

- Execute shell commands remotely via HTTP
- Stream stdout and stderr line-by-line via Server-Sent Events (SSE)
- Bearer token authentication
- Configurable per-command timeout
- Optional working directory restriction
- Cross-platform: Linux (arm64/amd64), Windows (amd64), Termux on Android

## Installation

Download the binary for your platform from the [releases page](https://github.com/newo-ether/conch/releases), or build from source:

```sh
git clone git@github.com:newo-ether/conch.git
cd conch
make all
```

Binaries are placed in `build/`.

## Usage

```sh
CONCH_API_KEY=my-secret-token CONCH_PORT=8080 ./conch-linux-amd64
```

### Configuration

All settings are passed via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `CONCH_PORT` | `8080` | HTTP listen port |
| `CONCH_API_KEY` | _(empty)_ | Bearer token for auth. If empty, all requests are allowed without authentication. |
| `CONCH_TIMEOUT` | `30` | Default command timeout in seconds |
| `CONCH_MAX_TIMEOUT` | `120` | Maximum allowed timeout in seconds |

## API

### `POST /execute`

Execute a shell command and stream the output.

**Headers:**
- `Authorization: Bearer <token>` — required if `CONCH_API_KEY` is set
- `Content-Type: application/json`

**Request body:**
```json
{
  "command": "ls -la /tmp",
  "timeout_ms": 10000,
  "workdir": "/tmp"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `command` | string | yes | The shell command to execute |
| `timeout_ms` | integer | no | Per-command timeout in milliseconds (capped at `CONCH_MAX_TIMEOUT`) |
| `workdir` | string | no | Working directory for the command |

**Response:** `text/event-stream` (SSE)

```
event: line
data: {"line":"drwxr-xr-x  2 user group 4096 Jan 1 12:00 .","stream":"stdout"}

event: line
data: {"line":"drwxr-xr-x 10 user group 4096 Jan 1 12:00 ..","stream":"stdout"}

event: result
data: {"exit_code":0}
```

**Error events:**
```
event: error
data: {"message":"command timed out"}
```

**HTTP status codes:**
- `200` — command executed (see SSE events for outcome)
- `401` — missing or invalid API key
- `405` — method not allowed (only POST is accepted)

### `GET /health`

Returns server health status. No authentication required.

```json
{"status":"ok"}
```

## Example

```sh
# Start the server
CONCH_API_KEY=testkey CONCH_PORT=9090 ./conch-linux-amd64 &

# Run a command
curl -N -H "Authorization: Bearer testkey" \
  -X POST http://localhost:9090/execute \
  -d '{"command":"echo hello && echo world >&2","timeout_ms":5000}'

# Output:
# event: line
# data: {"line":"hello","stream":"stdout"}
# event: line
# data: {"line":"world","stream":"stderr"}
# event: result
# data: {"exit_code":0}
```

## Cross-Compilation

```sh
make build-linux-arm64    # Termux on Android, Raspberry Pi
make build-linux-amd64    # Linux servers
make build-windows-amd64  # Windows
make all                  # All targets
```

## License

MIT — see [LICENSE](LICENSE).
