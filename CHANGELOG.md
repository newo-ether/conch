# Changelog

All notable changes to Conch are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/newo-ether/conch/compare/v1.0.11...HEAD
[1.0.11]: https://github.com/newo-ether/conch/compare/v1.0.10...v1.0.11
[1.0.10]: https://github.com/newo-ether/conch/compare/v1.0.9...v1.0.10
