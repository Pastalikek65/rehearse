# Roadmap

## Working MVP

Miniflux 2.2.19 → 2.3.3 rehearsal from a custom PostgreSQL archive, three independent restore environments, fixed API/data checks, private local state, JSON/HTML reports, and ownership-checked cleanup.

Preview preparation completed: independent implementation review, a real known-bad migration negative control, native Windows/WSL and Linux-in-WSL rehearsal tests, the synthetic archive and source CI evidence. Every release archive must separately pass post-build acceptance before publication; the release's `verification.json` identifies that immutable artifact and its results. The MVP remains an evaluation preview, not production v1.

## Beta

- Diagnose and recover interrupted runs without guessing resource ownership.
- Add local run history and machine-readable CI usage.
- Exercise cancellation, process interruption, malformed/large input, source stability and negative controls.
- Development source now checks cancellation during staged/source hashing and rechecks the source after cleanup; regression and independent review evidence precede the next beta release.
- Measure time and memory on documented synthetic datasets.
- Repeat clean-profile installation and primary-workflow acceptance for each future beta archive.

## Production v1

- Add a Forgejo adapter with repository and file integrity checks, including clean old-backup recovery.
- Publish a versioned adapter contract and compatibility tests.
- Complete independent security/product reviews and close critical/high findings.
- Qualify the published Windows/Linux x64 packages and provide release notes, SHA-256 values and a license inventory.

Additional adapters and arbitrary application versions require their own tested contracts. They are not promises of current compatibility.
