# Support and limitations

This is the 0.1 MVP evaluation preview. A source test passing does not qualify a downloadable package or every installation of a platform. This document records the tested environment and support boundaries; the release's attached `verification.json` records acceptance of each exact downloadable archive after it was built. `BUILD.json` intentionally retains its immutable build-time `packageAcceptance: not-run` status.

| Environment | Requirement | Current evidence |
| --- | --- | --- |
| Windows x64 | Explicit WSL2 distribution; Linux amd64 Docker Engine 28+ and Compose inside it | Native Windows executable completed the three-phase synthetic Miniflux rehearsal using a disposable Ubuntu 24.04.5 WSL2 engine (Engine 29.8.2, Compose 5.6.0). Exact archive acceptance is recorded separately in release verification. |
| Linux x64 | Local Docker Unix socket; Engine 28+ and Compose | Native Linux amd64 executable completed the three-phase synthetic rehearsal in Ubuntu 24.04.5 WSL2 through its local Unix socket (Engine 29.8.2, Compose 5.6.0). Ubuntu 24.04 CI also runs an actual rehearsal and its own extracted package. These are distinct artifacts; release verification identifies the downloadable archive. Physical Linux installations have not been locally qualified. |
| Other architectures, remote Docker endpoints | Unsupported | No qualification claimed. |

The supported application pair is Miniflux 2.2.19 → 2.3.3 with PostgreSQL 17.11. Images are pinned by digest. Version parsing does not imply that other syntactically valid versions are supported.

The preview was independently reviewed, and a synthetic negative control demonstrated that a known-bad migration fails and its owned resources are cleaned. [Exact-source CI evidence](https://github.com/Pastalikek65/rehearse/actions/runs/37847571499) covers Windows source/tests/vet/build, Linux source/race and the CI-built Linux package. The [release assets](https://github.com/Pastalikek65/rehearse/releases/tag/v0.1.0) include checksums, artifact-specific verification and a real synthetic JSON/HTML report. This evidence is scoped to the fixture and environments above; it is not production v1 certification.

## What a passing result covers

The selected backup restores into fresh baseline and target environments. Retained core data has the same streamed projection digest. Expected removed-entry conversion is checked separately. Authenticated read-only API calls succeed with structurally valid responses; anonymous access is rejected. The original backup restores into a fresh old-version recovery environment. The source remains unchanged, and owned resources are cleaned.

These checks concern the supplied backup and supported adapter. They do not test production configuration, integrations, RSS fetching, every account's credentials, or production traffic. The runner never connects to your production Miniflux instance.

## Current limits

- PostgreSQL custom-format backups only, with a 128 GiB input limit. Archive size does not predict restored disk usage.
- API responses are bounded to 8 MiB. Data projections are bounded to 1 GiB total and 4 MiB per row. Exceeding a limit fails the corresponding check.
- Windows requires an explicit WSL2 distribution and inherited private ACLs on the per-user state directory. No Windows power-loss durability guarantee is made.
- Process locks are never silently considered stale. Abnormal termination currently requires checking the recorded process before removing that exact lock directory. Automatic lock recovery is planned.
- In the released 0.1.0 preview, staged/original backup hashes are bounded by the file-size limit but are not cancellable mid-hash. Current development source checks cancellation between bounded reads and performs the final source rehash after its independent cleanup attempt. Cancellation cannot interrupt a filesystem read already blocked inside the operating system. These development changes are not part of the immutable 0.1.0 packages.
- Local state retains the staged backup after resources are cleaned. Remove the exact completed run directory when you no longer need its evidence and private backup.
- Outbound isolation has been tested against controlled HTTP, DNS and direct-IP targets. It is not a universal protocol or hostile-container security claim.
- No Forgejo adapter, user pilots, adoption statistics or v1 certification is claimed at this stage.

Reports require completed finalization and a matching JSON/HTML pair. A report/state/lock error makes the CLI exit nonzero and prevents it from advertising a completed passing result. Linux directory-sync errors after a filesystem operation are a durability uncertainty; the preview does not claim transactional power-loss recovery.
