# conch

A lightweight, zero-dependency shell execution server with SSE streaming and end-to-end encryption. Expose a shell on any device — Linux server, Termux (Android), Windows — and control it securely over the network. Ships with a companion MCP binary for Claude Desktop integration.

## Features

- **End-to-end encryption** — X25519 ECDH key exchange + AES-256-GCM, authenticated by a pre-shared API key
- **HMAC request signing** — proves API key possession without transmitting it
- **Anti-replay protection** — time-bucketed nonce tracking with ±5 min clock skew tolerance
- **SSE streaming** — stdout and stderr delivered line-by-line
- **Durable background jobs** — start, inspect, list, and stop commands independently of the client connection
- **Rate limiting** — per-IP token bucket (20 req/s sustained, burst 40)
- **Single binary** — zero dependencies, statically linked, ~8 MiB
- **Cross-platform** — Linux (arm64/amd64), Windows (amd64), Termux on Android
- **Managed background service** — system or per-user systemd (Linux), runit (Termux), nssm or a per-user Scheduled Task (Windows)
- **MCP support** — separate `conch-mcp` binary bridges Claude Desktop (stdio/JSON-RPC) to a remote Conch server

## Installation

The installers auto-detect the platform, download pre-built binaries from GitHub Releases, verify every download against `checksums.txt`, and generate an API key for a new install. A new Linux or Windows installation defaults to least-privilege `user` mode. When exactly one existing mode is detected, rerunning without a mode automatically updates that same mode while preserving the API key, configuration, durable jobs, service identity, and rollback binaries; use `--version vX.Y.Z` / `-Version vX.Y.Z` to pin a release.

Linux and Windows support two deliberately isolated modes:

- `user` (default for a new installation): a systemd user service or Windows Scheduled Task owned by the current user; no administrator/root access is required. It starts at login. On Linux, starting before login requires the administrator to explicitly enable linger.
- `system` (explicit): a machine-wide systemd or Windows service; administrator/root access is required.
- Updates automatically retain the one detected existing mode. An installer will not silently migrate between modes. If both modes exist or an existing custom prefix is ambiguous, specify the mode explicitly.

### Default per-user install

**Linux (no sudo):**
```bash
curl -fsSL https://raw.githubusercontent.com/newo-ether/conch/main/scripts/install.sh | bash
```

**Termux (Android):**
```bash
curl -fsSL https://raw.githubusercontent.com/newo-ether/conch/main/scripts/install.sh | bash
```

**Windows (ordinary PowerShell):**
```powershell
Set-ExecutionPolicy Bypass -Scope Process -Force; irm https://raw.githubusercontent.com/newo-ether/conch/main/scripts/install.ps1 | iex
```

### Explicit system install

**Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/newo-ether/conch/main/scripts/install.sh | sudo bash -s -- --mode system
```

**Windows (PowerShell as Administrator):**
```powershell
irm https://raw.githubusercontent.com/newo-ether/conch/main/scripts/install.ps1 -OutFile "$env:TEMP\install-conch.ps1"
& "$env:TEMP\install-conch.ps1" -Mode system
Remove-Item -LiteralPath "$env:TEMP\install-conch.ps1"
```

### Custom options

```bash
# Linux system mode
sudo ./scripts/install.sh --mode system --version v1.0.19 --port 14216 --api-key "your-key"

# Linux user mode
./scripts/install.sh --mode user --version v1.0.19 --port 14216 --api-key "your-key"

# Termux
./scripts/install.sh --port 8080
```

```powershell
# Windows system mode (run as Administrator)
.\scripts\install.ps1 -Mode system -Version v1.0.19 -Port 14216 -ApiKey "your-key"

# Windows user mode
.\scripts\install.ps1 -Mode user -Version v1.0.19 -Port 14216 -ApiKey "your-key"
```

### Uninstall or change modes

Uninstall only the intended mode. If both modes are present, the mode is mandatory; a custom prefix also requires an explicit mode.

```bash
# Linux
sudo ./scripts/install.sh --uninstall --mode system
./scripts/install.sh --uninstall --mode user
```

```powershell
# Windows
.\scripts\install.ps1 -Uninstall -Mode system   # Administrator
.\scripts\install.ps1 -Uninstall -Mode user
```

To change modes, uninstall the current mode first and then run the installer with the new mode.

### From source (manual)

```sh
git clone git@github.com:newo-ether/conch.git
cd conch

# Server
go build -o conch .
./conch  # CONCH_API_KEY=... required

# MCP binary
go build -o conch-mcp ./cmd/mcp/
```

### Cross-compilation

```sh
make all              # server: linux-arm64, linux-amd64, windows-amd64
make mcp-all          # MCP:    linux-arm64, linux-amd64, windows-amd64
```

Binaries land in `build/`.

## Quick start

```sh
export CONCH_API_KEY="your-secret-key"
./conch

# Or with the default per-user installer
./scripts/install.sh --api-key "your-secret-key"
```

Test:

```sh
curl -s http://localhost:14216/health
# {"status":"ok"}
```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `CONCH_HOST` | `0.0.0.0` | Listen address |
| `CONCH_PORT` | `14216` | HTTP listen port |
| `CONCH_API_KEY` | _(required)_ | Pre-shared key for HMAC signing and key-exchange verification. Server refuses to start without it unless `CONCH_ALLOW_NO_AUTH=true`. |
| `CONCH_ALLOW_NO_AUTH` | `false` | Start without an API key. Encryption and signing are disabled; all requests are accepted in plaintext. |
| `CONCH_TIMEOUT` | `30` | Default foreground wait budget in seconds; a foreground command still running when the budget is spent is not killed but reported as backgrounded |
| `CONCH_MAX_TIMEOUT` | `1800` | Maximum foreground wait budget, and the hard lifetime cap of any command, in seconds (30 minutes) |
| `CONCH_JOB_DIR` | OS user config directory | Directory containing durable snapshots of all shell executions |
| `CONCH_JOB_RETENTION_HOURS` | `24` | Retain completed foreground and background jobs for this many hours |
| `CONCH_MAX_JOB_TIMEOUT_SECONDS` | `1800` | Default runtime of one background job (30 minutes); a `timeout_ms` on `/jobs/start` overrides it per job. Configurable up to 7200 (2 hours) |
| `CONCH_MAX_JOB_OUTPUT_BYTES` | `262144` | Maximum rolling output retained per job |
| `CONCH_MAX_JOBS` | _(ignored)_ | Deprecated; snapshots expire only by time, never by count or acknowledgement |
| `CONCH_TRUSTED_PROXIES` | _(empty)_ | Comma-separated proxy IPs/CIDRs allowed to supply `X-Forwarded-For`; untrusted forwarding headers are ignored |

## Platform behavior

| Platform | Shell | Service manager | Process control |
|----------|-------|-----------------|-----------------|
| Linux | `/bin/sh` | systemd | dedicated process-group kill |
| Termux | `/bin/sh` | runit | dedicated process-group kill |
| Windows | non-interactive PowerShell (UTF-8) | nssm | Job Object with process-tree fallback |

When `workdir` is omitted, the command inherits the service working directory; pass an explicit absolute directory when location matters.

## Protocol

### `GET /health`

No auth. Returns status plus build version, revision, and Conch protocol version.

### `GET /version`

No auth. Returns the binary name, version, revision, deterministic build time, protocol version, and negotiated capability list.

### `GET /public-key`

Returns the server process's current X25519 public key, signed with HMAC-SHA256(key, nonce|public_key) to prevent MITM substitution. Clients refresh it only after an explicit pre-dispatch decryption rejection, so a restart cannot make MCP permanently stale:

```json
{
  "public_key": "<base64url 32B>",
  "nonce": "<base64url 12B>",
  "signature": "<64 hex chars>"
}
```

Clients verify this signature before trusting the key.

### `POST /execute`

Execute a shell command and retain its job snapshot under `CONCH_JOB_DIR`. Line and terminal events carry `job_id`, retrievable through `/jobs/get` until time retention expires. The terminal event is published only after its snapshot is durable. Disconnecting cancels this foreground process while its wait budget is unspent, then drains and retains its final state. When the wait budget is spent with the command still running, the server sends a `backgrounded` event carrying `job_id` and ends the response; the process keeps running under the hard cap (`CONCH_MAX_TIMEOUT`) and the client tracks it through the job endpoints. Every request carries an HMAC signature that proves API key possession.

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
| `description` | string | no | Purpose of this execution, retained with the job (4 KiB UTF-8 maximum); required by current tool schemas |
| `timeout_ms` | integer | no | Foreground wait budget in milliseconds (capped at `CONCH_MAX_TIMEOUT`); a command still running when it is spent continues as a background job |
| `workdir` | string | no | Working directory for the command |

**Response:** `text/event-stream` with encrypted data payloads (each `data:` line is base64url-encoded AES-256-GCM ciphertext):

```
event: line
data: <base64url ciphertext>

event: result
data: <base64url ciphertext>
```

**Error responses:** `401` for all auth failures (signature mismatch, clock skew, replayed nonce, missing headers). `400` for decryption errors. `405` for wrong method.

### Background job endpoints

Background jobs use the same authenticated and encrypted POST envelope as `/execute`. They continue after the initiating HTTP request disconnects, share the global command concurrency limit, and persist bounded output and terminal state under `CONCH_JOB_DIR`.

| Endpoint | Request body | Description |
|----------|--------------|-------------|
| `POST /jobs/start` | `{"command":"...","timeout_ms":600000,"workdir":"/tmp"}` | Start a durable job and return its `job_id` |
| `POST /jobs/list` | `{"offset":0,"limit":100}` | List a bounded page of retained job metadata, newest first; output is omitted. Returns `total`, `offset`, `next_offset`, and `has_more` |
| `POST /jobs/get` | `{"job_id":"..."}` | Return current state, rolling output, exit code, and truncation metadata |
| `POST /jobs/stop` | `{"job_id":"..."}` | Cancel the job and its process tree |
| `POST /jobs/ack` | `{"job_id":"..."}` | Idempotently confirm durable terminal settlement; the snapshot remains queryable until time expiry. Live or settling jobs are rejected |

List pages default to at most 100 records and 8 MiB of encoded metadata, keeping encrypted responses within the client read bound even with escaped long commands. Use `next_offset` while `has_more` is true. Each page reflects current newest-first ordering; new jobs or expiry can shift offsets between calls. Paging never deletes stored jobs. Foreground and background jobs share retention measured from completion, bounded rolling output and the global execution concurrency limit.

Job states are `running`, `stopping`, `settling`, `succeeded`, `failed`, `timed_out`, `stopped`, and `interrupted`. `settling` means the process has ended but its terminal snapshot is still retrying durable persistence and cannot yet be acknowledged. `timed_out` means the runtime budget killed the process, as opposed to `failed` which reports the command's own nonzero exit or an execution error. A job that was active when Conch restarted is recovered as `interrupted`; Conch never reports an orphaned process as still running.

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

## Binary file transfer protocol

Authenticated `POST /file/transfer/{action}` uses the existing encrypted JSON request and signed encrypted response. A handler request is limited to 1 MiB, decoded chunks to 256 KiB, regular files to 1 GiB, and retained transfer entries to 64. Each request uses a fresh encrypted envelope. This HTTP API is used by Agora; it does not add a `conch-mcp` tool.

| Action | Request fields | Result |
|--------|----------------|--------|
| `capabilities` | none | `chunk_bytes`, `max_bytes` |
| `info` | absolute `path` | resolved `path`, `size`, `sha256` |
| `read` | absolute `path`, expected `size`, `offset` | at most one Base64 `data` chunk, `size`, `offset` |
| `begin` | random 32-hex `id`, absolute destination `path`, `size`, `sha256`, optional `overwrite` | private staging receipt, `state=writing`, `offset=0` |
| `chunk` | `id`, exact current `offset`, Base64 `data` | accepted next `offset` |
| `commit` | `id` | verified `path`, `size`, `sha256`, `state=committed` |
| `status` | `id` | current receipt and offset |
| `abort` | `id` | remove unpublished staging; keep a committed destination |

Successful results include `ok=true`; application failures contain `error`. Destination parents must exist. No-overwrite publication uses Linux `renameat2(RENAME_NOREPLACE)`, with an atomic hard-link fallback only when that operation is unsupported; other platforms use atomic create-if-absent hard linking. Replacement uses the platform atomic replacement primitive. Unsupported filesystems fail without a weaker fallback. The complete staged size and SHA-256 are verified and flushed before publication. Sources are never deleted. A same-server copy follows the same protocol.

Reads are stateless, not a frozen source handle. The client must hash all downloaded bytes against `info` before opening destination staging; changed source bytes must fail the copy. Base64 stays within the transport, never model arguments/results. Clients must not replay chunks or commit after uncertain delivery; use `status` to reconcile the accepted offset or completed receipt. `commit` returns the existing receipt if already committed. An unavailable/expired status leaves an uncertain commit outcome: inspect the destination before starting another transfer.

Staging is private in the target directory, with cleanup journals under `CONCH_JOB_DIR/file-transfers`. After 15 idle minutes, entries and staging expire; cleanup runs once a minute. Startup removes journaled orphan staging. Completed receipts are process-local and do not survive restart. Cleanup failures retain the journal for a later attempt. This is a bounded copy protocol, without cross-restart resume or a power-loss transaction guarantee.

## MCP (Claude Desktop integration)

The `conch-mcp` binary exposes bounded synchronous shell execution, explicit durable job lifecycle tools, atomic file operations, and device discovery over stdio Model Context Protocol. Devices may be offline at MCP startup and reconnect lazily; concurrent requests and MCP cancellation notifications are supported.

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
| `timeout_ms` | integer | yes | — | Foreground wait budget in milliseconds (1–1800000); the MCP request deadline is this value plus a small settlement margin, and the server enforces the 30-minute ceiling. A command still running when the budget is spent keeps running as a durable job and the result says so |
| `workdir` | string | no | — | Working directory |

Every `shell_*` tool requires `description` describing that call's purpose. Execute/start store it as job metadata; later list/get/stop/ack descriptions describe the current action and never replace the original job purpose. `shell_execute`, `shell_start`, and `shell_job_get` accept optional `head_lines` and `tail_lines` together. Their model-facing preview marks omitted middle lines and never duplicates overlap; retained output is unchanged. `shell_jobs` accepts `offset` and `limit` for paging.

Durable execution uses `shell_start`, `shell_jobs`, `shell_job_get`, `shell_job_stop`, and `shell_job_ack`; it is never entered by silently retrying a timed-out synchronous command. File tools are `file_read`, `view_image`, `file_write`, `file_edit`, `file_multi_edit`, `file_glob`, and `file_grep`. `file_edit` and `file_multi_edit` execute atomically on the target and optionally accept `expected_sha256`.

## License

Copyright (c) 2026 Newo Ether.

The current Conch source is licensed under the **GNU General Public License,
version 3 only (SPDX: GPL-3.0-only)**. See [LICENSE](LICENSE) for the full terms.
This declaration applies to project-owned source, scripts and documentation unless
a file explicitly states another license. Third-party components retain their own
copyright and license terms; see [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).

Forks and commercial use are welcome. When distributing covered modified versions,
preserve copyright and license notices, identify modifications, and provide the
complete corresponding source under GPLv3. The project license does not imply
upstream endorsement of a modified distribution.

The MIT grant on already-published Conch code remains valid. The last pre-transition
main revision is [19d910e9500092dd310e7d0cb2224c6ac7fdefb5](https://github.com/newo-ether/conch/tree/19d910e9500092dd310e7d0cb2224c6ac7fdefb5),
whose [historical MIT license](https://github.com/newo-ether/conch/blob/19d910e9500092dd310e7d0cb2224c6ac7fdefb5/LICENSE)
continues to cover that revision and earlier MIT releases. The current project is
not offered as a choice between MIT and GPL. This change applies prospectively.
