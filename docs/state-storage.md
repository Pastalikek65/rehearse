# Run state and backup recovery

The state store contains per-run intent, process ownership locks, and a private
staged copy of the selected PostgreSQL custom-format backup. It is local
rehearsal state; it is not a Docker ownership proof and it does not authorize a
resource deletion by itself.

## State directory

The caller must select a fixed per-account configuration directory (for
example, an application directory under the current user's configuration
directory). Do not accept an arbitrary state directory from run JSON or
Compose input. On Unix, newly created directories and files use mode `0700`
and `0600`. On Windows, Go inherits the directory ACL, so the caller must use a
per-user location whose inherited ACL is private to that account. The state
directory also contains a source path and backup digest; treat it as private.

The store syncs newly created identity and run entries on Unix before they can
authorize Docker work. On Windows, file contents are synced and normal process
interruption is recoverable, but this implementation does not promise
power-loss durability for directory renames. The run root should be beneath an
already existing private parent directory.

## Exclusive run ownership

Before plan, run, report mutation, backup staging/recovery, or cleanup, the
runner must acquire `Store.AcquireRunLock(runID)` and retain the returned
`RunLock` until the entire operation has stopped. Pass that same lock to
`Store.Save`, `Store.StageBackup`, and `Store.RecoverBackup`. These methods
serialize state mutations for that run. `Store.Create` is the exception: it
persists the initial `planned` intent before Docker work exists, and the caller
then acquires the lock before proceeding.

The lock is an atomically created `run.lock` directory containing the run ID,
installation owner ID, host name, process ID, start time, and a random release
token. An existing lock is never treated as stale or automatically adopted.
`Store.InspectRunLock` exposes the recorded host and process metadata for
diagnosis but does not clear the lock. If a process exits abnormally, an
operator must verify that the recorded process is no longer running on the
recorded host before manually removing only that run's `run.lock` directory.
If the metadata is missing or invalid, fail closed and verify the run manually
before removing the exact lock directory. Do not remove a lock while its
process may still be active.

This is a coordination boundary for concurrent Rehearse processes, not a
sandbox against another trusted host user with access to the same filesystem or
Docker daemon. The runner still has to verify the live daemon identity and
inspect each exact resource name, kind, and ownership labels before any Docker
cleanup action.

## Staging and recovering a backup

`StageBackup` copies the original into `backup.partial`, checks the custom
archive header and source stability, and computes SHA-256 and byte count. It
then saves a `pendingBackup` record before renaming the partial file to
`backup.dump`. Finally it commits `backup` and clears `pendingBackup`. This
ordering makes both process interruption points recoverable.

On a resumed run, while holding the run lock, call `RecoverBackup` before
starting a new stage:

- If there is no pending intent and only a regular `backup.partial`, recovery
  removes that exact orphan and restages from the supplied original path.
- If a pending record exists and `backup.partial` or `backup.dump` exists,
  recovery accepts it only when its byte count and digest match the persisted
  intent. A matching partial is renamed and the manifest is committed.
- If pending intent exists but neither file exists, recovery restages only from
  the same recorded source path and requires the original digest to match.
- An unknown `backup.dump`, a non-regular artifact, a symlink, conflicting
  files, a changed source, or digest mismatch stops recovery. Unknown
  destination content is never adopted or overwritten.
- `BACKUP_RECOVERY_NOT_NEEDED` means there was no interrupted staging state;
  the caller may continue to `StageBackup`. Other recovery errors require
  diagnosis and must not be ignored.

Before sending restore bytes to a container, call
`Store.OpenVerifiedBackup(runID)`. It rejects a missing, non-regular, or
mutated staged file, hashes and sizes the open descriptor, then rewinds that
same descriptor for streaming. The caller must stream from the returned file
handle and close it after restore; do not re-open the path after verification.
The digest detects accidental changes and corrupted state, not malicious
changes by the trusted account that owns the rehearsal directory.

## Safe error handling

The store returns short fixed error codes and does not include secrets, file
contents, or raw JSON in errors. In particular:

- `RUN_LOCK_HELD` requires process diagnosis; it is not permission to remove
  resources or the lock.
- `BACKUP_RECOVERY_REQUIRED` means call the explicit recovery API while holding
  the same run lock.
- `BACKUP_ORPHAN_UNSAFE`, `BACKUP_INTEGRITY_FAILED`, and
  `BACKUP_RECOVERY_SOURCE_CHANGED` require stopping and reporting the run.
- `RUN_LOCK_INFO_INVALID` means lock metadata cannot establish process
  ownership. Fail closed and use manual operator verification.
