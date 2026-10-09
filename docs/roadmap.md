# Roadmap

## Working MVP

The immutable public v0.1 release supports Miniflux 2.2.19 → 2.3.3 rehearsal from a custom PostgreSQL archive, three independent restore environments, fixed API/data checks, private local state, JSON/HTML reports, and ownership-checked cleanup. It does not include the development Forgejo adapter or archive/history/recovery commands.

Preview preparation completed: independent implementation review, a real known-bad migration negative control, native Windows/WSL and Linux-in-WSL rehearsal tests, the synthetic archive and source CI evidence. Every release archive must separately pass post-build acceptance before publication; the release's `verification.json` identifies that immutable artifact and its results. The MVP remains an evaluation preview, not production v1.

## Beta

- Development source adds bounded history, recovery of verified dead local process locks, and Forgejo archive/rehearsal commands. A synthetic Forgejo example and 25-check source-workflow results exist; Forgejo is not public v0.1 support and final packages/v1 qualification are pending.
- Continue exercising cancellation, process interruption, malformed/large input, source stability and negative controls.
- Development source checks cancellation during staged/source hashing and rechecks the source after cleanup; regression and independent review evidence precede the next beta release.
- Measure time and memory on documented synthetic datasets.
- Repeat clean-profile installation and primary-workflow acceptance for each future beta archive.

## Production v1

- Finish Forgejo failure/interruption scenarios, distributed package acceptance and independent v1 review. Baseline/target/old-backup recovery, repository/file API checks and cleanup passed together in the synthetic source workflow; keep release status pending until the remaining gates pass.
- Publish and qualify the versioned Forgejo adapter contract only after full-fixture evidence is complete.
- Complete independent security/product reviews and close critical/high findings.
- Qualify the published Windows/Linux x64 packages and provide release notes, SHA-256 values and a license inventory.

See the [Forgejo adapter contract](forgejo-adapter.md) and [history/recovery behavior](history-recovery.md). Additional adapters and arbitrary application versions require their own tested contracts; they are not promises of current compatibility.
