# Changelog

All notable changes to Conch are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.15] - 2026-08-21

### Changed

- Raised the default synchronous shell timeout ceiling from 120 seconds to 1800 seconds (30 minutes) across the server configuration and both installers.
- Kept the ordinary per-command timeout default at 30 seconds, so long-running calls still require an explicit `timeout_ms`.

### Verification

- Added regressions requiring the 30-second command default and 30-minute maximum, plus installer contracts that keep both platform defaults at 1800 seconds.

## [1.0.14] - 2026-08-18

### Fixed

- Rendered PowerShell 5.1 `Format-List` / `Format-Table` output correctly by buffering the internal formatting-object sequence and formatting it as a whole instead of per object, which previously threw a `NullReferenceException` from `out-lineoutput`.

### Verification

- Added a regression covering `Format-List` and `Format-Table` output, asserting exit 0, no `Object reference not set`, and the expected content.

## [1.0.13] - 2026-08-18

### Fixed

- Rendered PowerShell 5.1 parser errors as plain text instead of CLIXML when a command is not valid PowerShell (for example bash-style `||` input), by catching `[ScriptBlock]::Create` failures in the executor bootstrap.

### Verification

- Added a bash-style parse-error regression asserting no `#< CLIXML` and no `_xHHHH_` escapes, and strengthened the existing parser-error test with the same assertions.

## [1.0.12] - 2026-08-18

### Fixed

- Removed metadata-only `structuredContent` from every MCP tool result so clients that prefer structuredContent (per spec) display the full payload instead of an empty summary shell.
- Rendered PowerShell 5.1 stderr as plain text instead of CLIXML-serialized records with `_xHHHH_` escapes.
- Trimmed partial UTF-8 sequences at both ends of file_read ranges so limits and offsets that cut multi-byte runes return valid UTF-8 without U+FFFD replacement characters.
- Reported the whole-file line count for truncated file_read responses instead of a constant zero.
- Refreshed a stale cached checksum manifest once when a verified download fails, so releases updated under the same version string no longer strand installs on old hashes.
- Made Find-Nssm prefer the installer's own nssm.exe, avoiding the slow winget fallback on upgrades.
- Defaulted unattended prompts (CONCH_YES=1, redirected stdin, port held by an existing Conch process) so piped or elevated installs cannot hang waiting for input.

### Verification

- Added regressions for tool-result payload shape (including an encrypted file_read round trip), plain-text stderr with CJK messages, rune-boundary truncation and offsets, whole-file line counts, and installer hardening contracts.
- Passed gofmt, go vet, the full Go test suite, a 20-run stability gate, the race detector, and PowerShell 5 installer regressions.

## [1.0.11] - 2026-08-12

### Fixed

- Suppressed the false `file already closed` stream warning produced when Conch intentionally closes its owned pipe reader after the bounded detached-descendant drain window.
- Kept genuine non-EOF stream read failures visible while preserving output, exit codes, durable terminal state, and detached-process completion behavior.

### Verification

- Added Executor and durable JobManager regressions requiring bounded drain closure to complete silently.
- Repeated the retained-handle, terminal settlement, and high-volume tail-output contracts, then passed the complete test, vet, and PowerShell 5 installer gates.

## [1.0.10] - 2026-08-12

### Fixed

- Made Windows PowerShell 5.1 command transport reliably execute single- and double-quoted here-strings, literal `@` characters, following statements, and commands up to the existing 64 KiB limit.
- Preserved CJK text, accented characters, and emoji through explicit UTF-8 command input and output without requiring a BOM or temporary script file.
- Made PowerShell parser errors and nonzero process outcomes report failure instead of an empty successful result.
- Prevented detached descendants that retain inherited output handles from leaving completed durable jobs stuck in `running`, while preserving a bounded final output-drain window and root-process exit metadata.
- Kept Linux installer output machine-readable where callers consume checksum data, and hardened existing-installation upgrades and ASCII-only status output.

### Verification

- Added regressions for PowerShell here-strings, Unicode, parser failures, file side effects, maximum command size, retained descendant handles, durable terminal settlement, and high-volume tail output.
- Verified Linux and Windows tests, Linux race detection, vet, Bash and PowerShell installer regressions, deterministic six-target builds, release checksums, and provenance attestation.

[Unreleased]: https://github.com/newo-ether/conch/compare/v1.0.15...HEAD
[1.0.15]: https://github.com/newo-ether/conch/compare/v1.0.14...v1.0.15
[1.0.14]: https://github.com/newo-ether/conch/compare/v1.0.13...v1.0.14
[1.0.13]: https://github.com/newo-ether/conch/compare/v1.0.12...v1.0.13
[1.0.12]: https://github.com/newo-ether/conch/compare/v1.0.11...v1.0.12
[1.0.11]: https://github.com/newo-ether/conch/compare/v1.0.10...v1.0.11
[1.0.10]: https://github.com/newo-ether/conch/compare/v1.0.9...v1.0.10
