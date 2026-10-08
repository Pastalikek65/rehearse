# Roadmap

## Working MVP

Miniflux 2.2.19 → 2.3.3 rehearsal from a custom PostgreSQL archive, three independent restore environments, fixed API/data checks, private local state, JSON/HTML reports, and ownership-checked cleanup.

Before publishing the preview: independently review the implementation, demonstrate a real migration failure, qualify the native CLI on Windows/WSL and Linux, publish the synthetic example and test evidence.

## Beta

- Diagnose and recover interrupted runs without guessing resource ownership.
- Add local run history and machine-readable CI usage.
- Exercise cancellation, process interruption, malformed/large input, source stability and negative controls.
- Measure time and memory on documented synthetic datasets.
- Produce installation archives and validate their primary workflow in clean environments.

## Production v1

- Add a Forgejo adapter with repository and file integrity checks, including clean old-backup recovery.
- Publish a versioned adapter contract and compatibility tests.
- Complete independent security/product reviews and close critical/high findings.
- Qualify the published Windows/Linux x64 packages and provide release notes, SHA-256 values and a license inventory.

Additional adapters and arbitrary application versions require their own tested contracts. They are not promises of current compatibility.
