# Run history and dead-process recovery

The supported CLI includes `history` and `recover`; the immutable v0.1.0 package does not include them. Recovery only clears a verified dead local process lock; it does not resume the run or clean Docker resources.

Use history to inspect the local Rehearse run store without reading reports or backup contents:

    rehearse history
    rehearse history --limit 100
    rehearse history --json

History shows a bounded summary: run ID, creation time, stored status, resource count, and whether a staged or pending backup exists. It does not show backup paths or hashes, daemon identity, credentials, report content, or application data. The default is 20 rows and the maximum is 100. JSON includes truncation flags. A store scan is capped at 1,000 entries; if that cap is reached, the command exits nonzero and reports that more records may remain unseen.

A `report-finalization=pending` annotation (JSON: `reportFinalizationPending: true`) means the publication marker is present or cannot be inspected. The stored status can still say `completed` after a late finalization error; that status alone does not prove success. The report command refuses that run. Keep the marker and diagnose the failed operation; neither `recover` nor `cleanup` converts a partially finalized report into a pass.

Malformed records are not treated as a clean history. Records with a valid run ID appear with status invalid and a fixed error code. Unsafe or malformed child names are counted without printing those names. History exits nonzero when it observes invalid records or reaches the scan cap. Review the store using the manual diagnosis guidance in [state storage](state-storage.md); do not delete files based only on a summary row.

## Recover a dead local process lock

The recover command is an explicit operator action for one run:

    rehearse recover RUN_ID

Recovery validates the run ID, persisted run state, and schema 1 lock record. It only proceeds when the lock names this host and the recorded process is confirmed dead. It checks the process before and after atomically claiming the exact lock directory, then rechecks the claimed owner, run ID, hostname, and token before removing only the known lock metadata. A running run is marked interrupted only after recovery acquires a fresh canonical run lock and saves the new status.

The claim uses the stable `run.lock.recovery` directory so another `recover` command can finish after a process interruption. If the canonical lock and claim both exist, or the claim contains unexpected or malformed files, recovery fails closed and preserves them for review. An exact empty claim means the prior recovery had already verified process death and removed the lock metadata; recovery removes only that empty directory, then records `interrupted` when appropriate. If neither canonical lock nor claim exists, recovery reports `RUN_LOCK_NOT_FOUND`; history and the manual state diagnosis instructions remain the sources for deciding what to do next.

Recovery fails closed for a live process, unknown process state or permissions, a foreign host, malformed metadata, a changed claim, or a concurrent new lock holder. PID reuse is treated conservatively as a live process. A claim that cannot be verified is preserved or restored so Rehearse does not silently take over an uncertain lock. There is no age-based stale-lock rule or force option.

Run-lock creation, release, and recovery are serialized across current Rehearse processes by a persistent kernel-locked `.lock-mutation.guard` file in each run directory. Do not remove or replace that guard file. Before upgrading Rehearse, stop its older processes; mixed-version lock operations are not supported.

Recovery does not resume the rehearsal, mark it passed, or remove Docker resources. A run with a pending backup intent remains in staging. After a successful recovery, inspect history and use the existing cleanup command separately when appropriate:

    rehearse cleanup RUN_ID

On Windows, cleanup still requires the same explicitly named WSL2 distribution used by the run. Never use a global Docker prune to remove rehearsal resources. If recovery reports an error or history shows invalid state, stop and follow the manual state diagnosis guidance before attempting cleanup.
