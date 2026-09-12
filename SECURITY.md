# Security boundary and current qualification

The pre-shared API key is the deliberate authentication root. Knowing that key grants
the configured shell/file capabilities. Its authorized holder or administrator being
compromised is not itself a protocol defect. Network validation assumes an attacker
without the key can observe, replace, redirect, repeat, reorder and truncate HTTP.
Do not add authentication prompts to compensate for that explicit trust model.

## Protocol requirements

- Current clients verify a fresh, API-key-authenticated challenge and the required
  security capabilities before dispatching any mutation. A cached public key alone
  is insufficient. Capabilities are protocol features, not software-version pins.
- X-Encryption v2 derives session keys using HKDF-SHA256 Extract and Expand with
  the conch-agora-v2 context. Servers retain explicitly requested v1 derivation for
  server-first upgrades; current clients never downgrade. Old clients do not acquire
  the new client-side guarantees merely by connecting to an updated server.
- Authenticate method, escaped path and query, timestamp, nonce, ciphertext and ephemeral client key
  before dispatch. Reject replayed and expired requests without invoking an executor.
- Refresh the authenticated key before a mutation and transmit the mutation once.
  Never automatically replay it after transmission, even after a signed rejection:
  a restarted server cannot establish what its previous process already executed.
  Only read operations can refresh and retry on an authenticated rejection bound to
  the request, HTTP status and response bytes. Normal key rotation remains supported.
- Bound each unauthenticated request body before allocation and share eight read
  slots across authenticated routes. Expired timestamps are refused before reading.
  Release the read slot before dispatch so long streams do not occupy that budget.
- Authenticate SSE event kind and a monotonic per-request sequence inside AES-GCM.
  Missing, duplicated, reordered, renamed, cross-request and post-terminal events
  fail. EOF alone is not a successful terminal event. Clients must enforce framing;
  old clients ignoring the additional JSON fields do not gain this protection.
- Reject HTTP redirects for authenticated agent transport. Bound response bodies
  and individual event lines before allocating strings, parsing JSON or decrypting.
- Authenticate response direction, HTTP status and ciphertext with a distinct HMAC
  subkey derived from the current request's session key. Successful decryption alone
  is insufficient: a reflected request previously became a successful empty file read.
  Do not fall back to accepting an unsigned response from an older server.
- An attack that can drop traffic can prevent communication. Do not mistake this
  unavoidable availability limit for permission to retry an uncertain mutation.

## Windows credentials

Service env files, backups and NSSM AppEnvironmentExtra registry keys permit only
Administrators and SYSTEM. Set permissions before placing secrets in a new file or
registry key. Preserve key bytes during installation, updates and permission repair.
Never print keys in audit evidence. Existing ordinary client processes may use their
already configured credentials; they must not depend on reading service secret files.

## Qualification evidence

The 2026-09-11 isolated audit reproduced a forged plaintext stale-key error causing
two executions of one accepted command, and duplicated encrypted SSE data being
accepted twice. The candidate requires an authenticated rejection and sequenced
events. Regression tests exercise legitimate rotation, forged/cross-request proofs,
redirect refusal and duplicate events. Go test and vet passing are source evidence;
fleet deployment and Android enforcement must be tracked separately.

The 2026-09-12 audit additionally reproduced a genuine signed restart rejection
causing two executions: an intermediary replayed an already accepted packet to a
new server process, then supplied that process's valid rejection to the client.
Mutation retries are removed; a fresh preflight preserves ordinary key rotation.
Raw query tampering, aggregate authentication reads, release/recovery of read slots,
expired-header early rejection and cross-language X25519/HKDF vectors are covered.
Source, complete suites, race checks, build hashes and installed versions are separate
qualification steps recorded in the delivery evidence. A passing candidate is not
evidence that every deployed client and server already enforces the protocol.

Additional audit scope includes pre-authentication concurrency/memory, cancellation,
nonce lifecycle, oversized/chunked/compressed bodies, traffic binding and file-transfer
limits. This document is a contract and an audit record, not a claim of complete
security certification or a substitute for testing the installed versions.
