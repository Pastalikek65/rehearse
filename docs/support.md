# Support and limitations

Rehearse 0.2.0 is a public beta for two fixed application pairs: Miniflux 2.2.19 → 2.3.3 and Forgejo 15.0.9 → 16.0.5. Both use PostgreSQL 17.11 and pinned Linux amd64 images. Other versions and adapters are unsupported. Production v1 has not been published or qualified.

Download the [Windows x64 ZIP](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/rehearse-0.2.0-windows-amd64.zip) or [Linux x64 tar.gz](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/rehearse-0.2.0-linux-amd64.tar.gz). The [release verification record](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/verification.json) binds post-build results to each exact archive; `BUILD.json` inside each archive retains its original `packageAcceptance: not-run` value. The archives are unsigned. Check [SHA256SUMS](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/SHA256SUMS) before use.

The v0.2.0 packages each passed both bundled synthetic rehearsals: 23 Miniflux checks and 25 Forgejo checks. Both packages also passed their platform-specific interruption and recovery acceptance. The release record contains package digests, run IDs, result digests, and inventory checks. Attached [Forgejo HTML](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.html), [Forgejo JSON](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.json), [Miniflux HTML](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/miniflux-example-report.html), and [Miniflux JSON](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/miniflux-example-report.json) reports are actual Linux package outputs.

| Environment | Requirement | Acceptance evidence |
| --- | --- | --- |
| Windows x64 | Explicit WSL2 distribution with Linux amd64 Docker Engine 28+ and Compose | Native Windows package passed both bundled rehearsals and its interruption/recovery checks using Ubuntu 24.04.5 WSL2, Engine 29.8.2, and Compose 5.6.0. |
| Linux x64 | Local Docker Unix socket; Engine 28+ and Compose | Native Linux package passed both rehearsals and interruption/recovery checks in Ubuntu 24.04.5 WSL2. Ubuntu 24.04 CI also exercises source, race tests, and the extracted Linux package. Physical Linux installations have not been locally tested. |
| Other architectures or remote Docker endpoints | Unsupported | No qualification claimed. |

The [exact-source CI run](https://github.com/Pastalikek65/rehearse/actions/runs/37862540651) passed all four jobs for the release source commit. A separate [CI negative-control run](https://github.com/Pastalikek65/rehearse/actions/runs/37864445448) also exercised a real restored-target Forgejo migration failure. CI results do not replace the release record for the downloadable archives. The immutable [v0.1.0 preview](https://github.com/Pastalikek65/rehearse/releases/tag/v0.1.0) remains Miniflux-only and does not include Forgejo, `archive`, `history`, or `recover`.

## What a passing result covers

For Miniflux, the selected backup restores into fresh baseline and target environments. Retained core data has the same streamed projection digest. Expected removed-entry conversion is checked separately. Authenticated read-only API calls succeed with structurally valid responses, anonymous access is rejected, and the original backup restores into a fresh old-version recovery environment. The source remains unchanged, and Rehearse removes resources it proves it owns.

For Forgejo, the fixed ZIP archive is checked and restored into fresh baseline, target, and old-version recovery environments. The report compares bounded global SQL projections and Git repository-file projections, checks selected authenticated API content and anonymous rejection, verifies the source remains unchanged, and records owned cleanup. See the [Forgejo adapter contract](forgejo-adapter.md) for the exact fields, files, endpoints, and exclusions.

These checks concern the supplied synthetic fixtures and the documented adapter contracts. They do not test production configuration, integrations, RSS fetching, every repository object or account, full application functionality, or production traffic. Rehearse is a trusted local runner, not a sandbox for hostile containers. Qualification used synthetic fixtures only. When rehearsing your own backup, protect its private contents and use a separate rehearsal engine; never attach production Docker resources.

## Known issues and operator guidance

Three intermittent Windows state/staging errors occurred during synthetic development tests: `STATE_WRITE_FAILED`, `BACKUP_FAILED`, and `OPERATION_FAILED`. Their original operation and operating-system cause were not captured and remain unknown. Later successful repetitions do not prove a fix. No source-data loss or false passing report was demonstrated. The release verification record keeps this P2 finding open.

A controlled Windows test also reproduced an actual file-share denial during state-file replacement. Rehearse returned `STATE_WRITE_FAILED`, preserved the source, rejected the unstaged backup, and allowed explicit restaging after the open handle was closed. This establishes a safe response to that controlled failure; it does not identify the historical cause.

If a state write fails, stop concurrent Rehearse use and preserve the private run directory. Check available storage and access permissions, and identify any process holding a state file before closing it. Use `history` and `report` to inspect the run. Use `recover RUN_ID` only when its documented checks confirm a dead local process; then use `cleanup RUN_ID` separately if the recorded resources are safe to remove. Never remove a live or ambiguous lock, delete `report.finalizing`, or edit state files to make a report readable. If the cause remains unclear, retain the state for diagnosis and start a new run only after the storage issue is resolved.

## Limits

- Miniflux accepts PostgreSQL custom-format backups up to 128 GiB. Forgejo accepts its documented ZIP format up to 2 GiB; see the [adapter contract](forgejo-adapter.md) for member and expanded-data bounds. Archive size does not predict restored disk usage.
- API responses are bounded to 8 MiB. Miniflux data projections are bounded to 1 GiB total and 4 MiB per row. Exceeding a limit fails the corresponding check.
- Windows requires an explicitly named WSL2 distribution with Docker Engine and Compose inside it. Use a per-user state directory with inherited private ACLs. Windows directory operations do not carry a power-loss durability guarantee.
- The beta includes `history` and verified dead-local-process `recover`. The immutable v0.1.0 package lacks both commands; its abnormal-termination instructions differ. See [history and recovery](history-recovery.md).
- The v0.1.0 package does not cancel a hash already in progress. The beta checks cancellation between bounded reads and rehashes the source after cleanup. Cancellation cannot stop a filesystem read already blocked in the operating system.
- Local state retains the staged backup after Docker cleanup. Remove only the exact completed run directory when you no longer need its report and private backup.
- Controlled outbound tests covered HTTP, DNS, and direct-IP targets through the Windows-to-WSL engine. Separate native-Linux egress-canary evidence remains pending. These tests do not establish universal network isolation or a hostile-container security boundary.
- Forgejo's 25 checks cover the fixed synthetic profile, selected repository files, and selected API behavior. They do not establish full application-data equivalence. The archive command packages operator-supplied files but cannot prove that the database and data TAR form one consistent snapshot.
- No user pilot, production v1 certification, or production-data test is claimed.

Reports require completed finalization and a matching JSON/HTML pair. A report, state, or lock error makes the CLI exit nonzero and prevents it from advertising a completed passing result. Linux directory-sync errors after a filesystem operation leave durability uncertain; the beta does not promise transactional power-loss recovery.

The beta writes a `report.finalizing` marker before publishing a report. The report command rejects any run with that marker, including when error-path report deletion fails. Rehearse removes the marker only after terminal state saving and lock release succeed. On error, it also attempts to invalidate both report files and save a failed status while the lock remains held. Preserve a failed finalization for manual diagnosis. Do not remove its marker or treat its stored status as proof of success. This process-error guard does not promise transactional power-loss recovery.
