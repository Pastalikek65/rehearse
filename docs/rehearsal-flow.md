# Miniflux rehearsal flow

The Miniflux adapter supports one fixed upgrade pair: Miniflux 2.2.19 to 2.3.3, with
PostgreSQL 17.11. A run uses only the pinned Linux/amd64 image references in
`internal/miniflux`. It creates three separate, empty database volumes and
isolated networks. The application and probe containers have no published
host ports. Only the probe container calls the application, at
`http://app:8080`, over the phase's private network.

The runner validates the fixed configuration, backup header, and one complete
API authentication method before it records a run or asks Docker to create
resources. It stages the backup in the private run directory and records its
SHA-256 and byte count. Every TOC inspection and database restore opens a
verified handle to those staged bytes. The source file is hashed and measured
again before cleanup; a content or size difference fails the run.

The three phases are deliberately independent:

1. **Baseline:** create an empty PostgreSQL volume, verify `SELECT 1`, inspect
   the staged custom-format archive with `pg_restore --list` from stdin, then
   restore it with `--no-owner --no-acl --exit-on-error`. Confirm schema 125,
   capture the database projection, then start Miniflux 2.2.19 and run the
   authenticated API probes.
2. **Target:** restore the same staged archive into a new empty volume, verify
   it still starts at schema 125, and run only the target image's migration.
   Confirm schema 132, compare retained database data and tombstone conversion,
   then start Miniflux 2.3.3 and repeat the API probes.
3. **Recovery:** restore the original staged archive into a third empty
   volume. Do not migrate it. Confirm schema 125, the complete source
   projection and removed-entry set, then start Miniflux 2.2.19 and repeat the
   API probes.

The projection emits sorted JSON rows for users, categories, feeds, and
unread/read entries. Entry rows include identity, feed/user ownership, hash,
published and changed timestamps normalized to UTC, title, URL, author,
content, status, starred state, tags, and comments URL. The source and
recovery projections must match the target projection and each other. Counts
also compare users, categories, feeds, unread/read entries, starred entries,
and tagged entries. The projection intentionally leaves out rows with
`status='removed'`: the target migration converts eligible removed entries
into `entry_tombstones`. Rehearse compares the target tombstone key fingerprint
to the source's eligible `(feed_id, hash)` keys and requires the target removed
row count to be zero. Removed-entry conversion is reported separately from the
retained-data fingerprint.

Fingerprinting streams each newline-delimited JSON row into SHA-256 and records
row and byte totals. It does not accumulate the whole query output. The stream
has a 1 GiB total limit and a 4 MiB per-row limit; an unterminated final row is
an error. SQL results that are expected to be small use bounded output buffers.

For each application phase, the probe performs authenticated GET requests to
`/v1/me`, `/v1/version`, categories with counts, feeds, unread entries, read
entries, and starred entries. It validates the expected application version,
user identity, item ownership, response structure, list totals, and category
aggregates. Entry endpoints request at most 100 rows; their API totals are
checked, while the database fingerprint provides the all-row comparison. The
same user ID, username, and API count observation must hold across all three
phases. A separate unauthenticated `/v1/me` request must return HTTP 401.
Credentials travel to curl through a fixed stdin config; they are not placed
in process arguments, run state, or reports.

Each check is recorded as passed, failed with a fixed evidence code, or
not-run. A failed prerequisite stops dependent checks and gives them no
success status. A complete report can say `passed` only when all required
checks pass, the staged backup has a valid digest, and the three non-empty
core fingerprints match. Cleanup runs with its own bounded context even when
the caller cancels or a phase fails. Resources are removed only after the
engine verifies their recorded ownership. If that proof or cleanup fails,
the report records `CLEANUP_HELD` and the resources remain available for
explicit cleanup.

The JSON and HTML reports contain fixed check codes, versions, platform,
backup digest and size, and core fingerprint summaries. They do not contain
feed or entry contents, credentials, source paths, or raw subprocess output.
Reports are written with private file permissions under the private run
directory. A report only describes this local synthetic or user-supplied
backup rehearsal; it is not a claim that an external production service was
tested.
