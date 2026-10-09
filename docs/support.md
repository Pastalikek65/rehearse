# Support and limitations

Rehearse supports two fixed application pairs: Miniflux 2.2.19 → 2.3.3 and
Forgejo 15.0.9 → 16.0.5. Both use PostgreSQL 17.11 and pinned Linux amd64
images. Other versions and adapters are unsupported. A version parser
accepting a value does not make that version pair supported.

Download archives from [GitHub Releases](https://github.com/Pastalikek65/rehearse/releases).
Compare an archive with that release's `SHA256SUMS` and consult its
`verification.json`. The [v1.0.0 verification record](https://github.com/Pastalikek65/rehearse/releases/download/v1.0.0/verification.json)
is authoritative for post-build acceptance of those exact archive digests and
the environments recorded there. `BUILD.json` is immutable build metadata;
its `packageAcceptance: not-run` value is not a runtime test result. Archives
are unsigned.

## Runtime environments

| Platform | Required Docker endpoint | Scope and limits |
| --- | --- | --- |
| Windows x64 | Docker Engine 28+ and Compose inside an explicitly named WSL2 distribution | Rehearse does not use the Windows Docker Desktop pipe or install/start an engine. Check the release verification record for the exact Windows and WSL distribution tested. |
| Linux x64 | Docker Engine 28+ and Compose through the local Unix socket at `/var/run/docker.sock` | Acceptance applies to the environment stated in the release record. WSL2 or CI results do not qualify every physical Linux distribution. |
| Other architectures or remote Docker endpoints | Unsupported | No compatibility or acceptance is implied. |

The native-Linux controlled egress canary ran in Ubuntu 24.04.5 inside WSL2
through its local Unix Docker socket, using Engine 29.8.2. Its ordinary-network
control reached synthetic IPv4 HTTP services through the host gateway and
direct IP (HTTP 200 with the expected body twice) and reached a synthetic DNS
A-record service (one query). In isolated mode, HTTP returned no body and made
no request to the server; DNS failed with `NETWORK_UNREACHABLE` and made no
query. The test covered only host-gateway/direct-IP HTTP and direct
DNS-over-UDP on port 53. It did not contact external destinations or test
IPv6. Consult the release verification record for the exact run and cleanup
evidence. This bounded test is not a universal protocol-isolation guarantee
or a hostile-container security boundary.

## What the rehearsal checks

For Miniflux, the selected backup restores into fresh baseline and target
environments. Rehearse compares retained core data, checks the expected
removed-entry transformation, performs fixed authenticated read-only API
requests, rejects anonymous access, restores the original backup into a fresh
old-version recovery environment, and verifies source immutability and owned
cleanup.

For Forgejo, the fixed ZIP archive is validated and restored into fresh
baseline, target, and old-version recovery environments. The report compares
bounded global SQL and Git repository-file projections, checks selected
authenticated API content and anonymous rejection, verifies source
immutability, and records owned cleanup. The [Forgejo adapter contract](forgejo-adapter.md)
lists the exact fields, files, API operations and exclusions.

Published qualification used synthetic fixtures only. The checks do not cover
production configuration, integrations, RSS fetching, every repository object
or account, full application functionality, or production traffic. You may
rehearse a backup you are authorized to use, including a backup from a
production instance, but keep the staged copy and local state private and use
a separate rehearsal engine. Never attach production Docker resources or
point Rehearse at a production Docker endpoint. Rehearse is a trusted local
runner, not a sandbox for hostile containers.

## Known issue: Windows state and staging reliability

Three intermittent Windows state/staging errors occurred during synthetic
development tests: `STATE_WRITE_FAILED`, `BACKUP_FAILED`, and
`OPERATION_FAILED`. Their original operation and operating-system cause were
not captured and remain unknown. Successful repetitions do not prove a fix.
No source-data loss or false passing report was demonstrated. The current
release verification record retains this P2 reliability finding.

A separate controlled Windows test reproduced a file-share denial during
state-file replacement. Rehearse returned `STATE_WRITE_FAILED`, preserved the
source, rejected the unstaged backup, and allowed explicit restaging after the
open handle was closed. This demonstrates safe behavior for that controlled
failure; it does not identify the cause of the earlier events.

If a state write fails, stop concurrent Rehearse use and preserve the private
run directory. Check available storage and access permissions, and identify
any process holding a state file before closing it. Use `history` and `report`
to inspect the run. Use `recover RUN_ID` only when its documented checks
confirm a dead local process; then use `cleanup RUN_ID` separately if the
recorded resources are safe to remove. Never remove a live or ambiguous lock,
delete `report.finalizing`, or edit state files to make a report readable. If
the cause remains unclear, retain the state for diagnosis and start a new run
only after the storage issue is resolved.

## Limits

- Miniflux accepts PostgreSQL custom-format backups up to 128 GiB. Forgejo
  accepts its documented ZIP format up to 2 GiB; see the adapter contract for
  member and expanded-data bounds. Archive size does not predict restored disk
  usage.
- API responses are bounded to 8 MiB. Miniflux data projections are bounded to
  1 GiB total and 4 MiB per row. Exceeding a limit fails the corresponding
  check.
- Windows requires an explicitly named WSL2 distribution with Docker Engine
  and Compose inside it. Use a per-user state directory with inherited private
  ACLs. Windows directory operations do not carry a power-loss durability
  guarantee.
- Process locks are never silently considered stale. `recover` handles only a
  verified dead local process lock and does not resume a run or remove Docker
  resources. See [history and recovery](history-recovery.md).
- Cancellation is checked between bounded reads. It cannot stop a filesystem
  read already blocked inside the operating system.
- Local state retains the staged backup after Docker cleanup. Remove only the
  exact completed run directory when you no longer need its report and private
  backup.
- Forgejo's checks cover the fixed synthetic profile, selected repository
  files, and selected API behavior. They do not establish full application
  data equivalence. The archive command packages operator-supplied files but
  cannot prove that the database and data TAR form one consistent snapshot.

Reports require completed finalization and a matching JSON/HTML pair. A report,
state, or lock error makes the CLI exit nonzero and prevents it from
advertising a completed passing result. Linux directory-sync errors after a
filesystem operation leave durability uncertain; Rehearse does not promise
transactional power-loss recovery.

Rehearse writes a `report.finalizing` marker before publishing a report. The
report command rejects any run with that marker, including when error-path
report deletion fails. Rehearse removes the marker only after terminal state
saving and lock release succeed. On error, it also attempts to invalidate both
report files and save a failed status while the lock remains held. Preserve a
failed finalization for manual diagnosis. Do not remove its marker or treat
its stored status as proof of success. This process-error guard does not
promise transactional power-loss recovery.
