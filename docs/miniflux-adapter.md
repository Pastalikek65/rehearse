# Miniflux adapter contract

The supported upgrade is Miniflux 2.2.19 (schema 125) to 2.3.3 (schema 132), using PostgreSQL 17.11 on Linux/amd64. The adapter pins the platform-specific image manifests; the Go runner does not accept caller-selected images.

| Role | Immutable Linux/amd64 image reference |
| --- | --- |
| Source | `docker.io/miniflux/miniflux@sha256:8e1ab958250c86b1ce7081cd5f875e60b11fd2833fcb9177f972e45dce18fade` |
| Target | `docker.io/miniflux/miniflux@sha256:4c24c30b8b420c77e3d074bfb6d78cfe5d37670f895fa68c09c43cb719b9ab04` |
| Known-bad migration fixture | `docker.io/miniflux/miniflux@sha256:692c7376cbd42b066697e33201b7afa51bc6fe388b10939c628e4c58647b1670` |
| PostgreSQL | `docker.io/library/postgres@sha256:3cec7eb015ba8adb28139fa5c83b8489cdf0e666e53dfdf20f598ae0cc8739e3` |
| API probe | `docker.io/curlimages/curl@sha256:43366cd60f226c7655181a0f7e85c468a41d182fdd2dc2c1c3b872a2b9d05d7a` |

The digest references identify the Linux/amd64 image manifests, rather than mutable tags or multi-platform index references. The source and target migration arrays contain 125 and 132 migrations respectively. Miniflux creates `schema_version(version text not null)` and stores the decimal migration number in that text column; the adapter reads it with `SELECT version::integer FROM schema_version`.

## Database observations

`ProjectionSQL` emits JSONB arrays, one row per line, ordered by record kind and numeric ID. It fingerprints users, categories, feeds, and retained read/unread entries. User `timezone` is a preference string unchanged by migrations 126–132; `last_login_at` is omitted because authenticated `/v1/me` probes can update it. Retained entry `published_at` and `changed_at` are included, normalized through explicit UTC formatting so session `TimeZone` cannot change the digest. Migration 127 moves `changed_at` only for removed entries, which are outside this projection and checked through tombstones. The runner should set `DISABLE_SCHEDULER_SERVICE=1` so background feed refreshes do not mutate fixture data. Entry rows include `starred` and `tags`; the source defines `starred` as a nullable boolean with a false default and `tags` as a nullable `text[]` with an empty-array default. JSON output keeps `NULL` distinct from `false` and from an empty array. The target adds feed and entry `language` in migration 132, so that new target-only field is not in the common projection.

`CountsSQL` reports user, category, feed, unread, read, removed, starred, and tagged-entry counts. Starred and tagged counts include only retained read/unread entries. Compare these active-entry and base-table counts across the upgrade. Removed-entry counts are expected to fall to zero in the target.

Migration 127 creates `entry_tombstones`, copies keys only for removed rows with a nonempty hash and an existing feed, then deletes every row with status `removed`. `RemovedKeysSQL` uses that same eligibility predicate; compare its ordered `(feed_id, hash)` rows with `TombstoneKeysSQL` after migration. This separately checks the intended conversion while the removed-entry count checks that no removed rows remain. `CountsSQL` does not reference the new table, so it runs on both schemas.

The known-bad 2.3.0 image is only for a deterministic failure fixture. Restore the synthetic backup to a disposable negative-phase database, then use a superuser SQL session to turn one synthetic retained entry into an orphaned removed entry while keeping the FK constraint installed:

```sql
BEGIN;
SET LOCAL session_replication_role = replica;
UPDATE entries
SET feed_id = 9223372036854770000,
    hash = 'rehearse-orphan-migration-127',
    status = 'removed'
WHERE id = (SELECT min(id) FROM entries WHERE status IN ('unread', 'read'));
COMMIT;
```

The fixture must contain at least one read/unread entry. Run the 2.3.0 image against this disposable restored database. Its migration 127 attempts the tombstone insert without excluding the orphan and fails the target table's foreign-key constraint. The 2.3.3 migration adds `feed_id IN (SELECT id FROM feeds)`, skips the orphan, and deletes the removed row. This exercises the upstream failure and fix without a product-only failure switch. Never use the mutated negative-phase volume as the successful target or recovery source; both are restored independently from the unchanged synthetic backup.

The primary recovery proof must restore the original synthetic backup into a separate fresh PostgreSQL volume and boot it with the pinned 2.2.19 source image. A successful process exit or health endpoint alone does not prove recovery: verify schema 125, compare counts and projection fingerprint with the baseline, and run authenticated API probes against the restored source. Do not downgrade the target volume.

## API probes and credentials

The fixed allowlist uses read-only `GET` requests:

| Endpoint | Evidence |
| --- | --- |
| `/v1/me` | Authentication resolves to the expected synthetic user. |
| `/v1/version` | The running application reports the expected version. |
| `/v1/categories?counts=true` | Categories and their unread/feed counts are readable. |
| `/v1/feeds` | Feed records are readable. |
| `/v1/entries?limit=100&status=unread` | An unread-entry sample is readable. |
| `/v1/entries?limit=100&status=read` | A read-entry sample is readable. |
| `/v1/entries?limit=100&starred=true` | A starred-entry sample is readable. |

Miniflux documents Basic authentication and the `X-Auth-Token` header, the `counts=true` category option, and `status`/`starred` entry filters. Entry API responses are capped and paginated; the 100-row probes are representative authenticated API checks, not proof that every row survived. The database fingerprint and counts provide the all-row checks for this fixture.

The adapter builds curl configuration for the fixed in-network origin `http://app:8080` only. The engine supplies `--disable --config -` and sends the config on stdin to avoid user credentials in process arguments. The config bypasses proxy environment variables, sets connection and total time limits, and leaves redirect following disabled. `Auth` excludes credential fields from JSON. API tokens are checked for control characters; Basic credentials are base64-encoded into the Authorization header so the password does not appear in the config text. A nil auth value is reserved for the negative unauthenticated probe.

`ParseResponse` accepts only bounded JSON output followed by curl's newline-delimited three-digit HTTP status code. It returns a fixed safe error code for malformed or oversized output and never includes raw response text in an error. `Fingerprint` accepts newline-delimited JSON, hashes each complete row and its newline incrementally with SHA-256, and returns only the digest, row count, and byte count. A final row without a newline is rejected. It buffers at most one 4 MiB row plus a fixed 64 KiB reader buffer and accepts at most 1 GiB total; a single API response is limited to 8 MiB.

## Upstream references

- [Miniflux 2.2.19 migration source](https://github.com/miniflux/v2/blob/2.2.19/internal/database/migrations.go) — source schema and common fields.
- [Miniflux 2.3.3 migration source](https://github.com/miniflux/v2/blob/2.3.3/internal/database/migrations.go) — migrations 127 and 132.
- [Miniflux 2.3.0 migration source](https://github.com/miniflux/v2/blob/2.3.0/internal/database/migrations.go) — known-bad migration 127 lacks the orphan-feed filter.
- [Miniflux 2.3.3 migration runner](https://github.com/miniflux/v2/blob/2.3.3/internal/database/database.go) — schema version read and update behavior.
- [Miniflux API reference](https://miniflux.app/docs/api.html) — auth, endpoint, filter, and pagination semantics.
- [Miniflux configuration reference](https://miniflux.app/docs/configuration.html) — `DISABLE_SCHEDULER_SERVICE` setting.
- [Miniflux releases](https://github.com/miniflux/v2/releases) — 2.3.0 removal/tombstone change and the reported orphan-entry migration fix.
- [curl 8.22.0 container source](https://github.com/curl/curl-container/tree/8.22.0) — pinned probe image build source.
