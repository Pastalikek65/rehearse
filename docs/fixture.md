# Synthetic Miniflux fixture

`internal/fixture` defines the fixed data used to qualify the Miniflux
2.2.19 (schema 125) to 2.3.3 (schema 132) rehearsal. The data is synthetic;
the fixture does not fetch feed URLs or use a real account.

## Fresh baseline setup

Initialize the fresh baseline database with Miniflux's environment-driven
admin-creation path: `CREATE_ADMIN=1`, `ADMIN_USERNAME=rehearse-fixture`, and
`ADMIN_PASSWORD=synthetic-fixture-password`. These are public fixture
credentials, not production secrets; never reuse them outside an isolated
rehearsal. The runner should use its fixed one-shot initialization command;
Miniflux's interactive `-create-admin` command is not used. Then send
`fixture.SeedSQL` to that same database through the runner's fixed SQL stdin
path. It does not accept SQL from user configuration.

Miniflux 2.2.19 `CreateUser` creates exactly one default category titled
`All` for this new administrator. The seed transaction requires exactly that
single `All` category owned by the fixture admin, `hide_globally=false`, and
empty `feeds` and `entries` tables. It deletes only that verified empty
default category inside the same transaction, then inserts two fixture
categories. Any additional, renamed, hidden, or differently owned category,
or any feed or entry, causes the seed to abort without changing the database.
The SQL inserts two feeds whose
URLs use the special-use `.invalid` domain (including its subdomains, per the
[IANA Special-Use Domain Names registry](https://www.iana.org/assignments/special-use-domain-names)),
then inserts four entries with explicit IDs. It advances each affected serial
sequence so any subsequent Miniflux inserts receive a larger ID. The user row itself must come from
Miniflux initialization; the fixture never inserts or edits a user or password.

The active records include one read, starred, tagged row and one unread row;
all fields selected by Miniflux's active-entry API query have values (including
an empty tags array and empty comments URL rather than SQL `NULL`). The content
includes Unicode and a newline. Nullable author, content, starred, tags, and
comments-URL values are kept on the eligible removed row, which active API
queries omit and migration 127 deletes after extracting its key. The other
removed row has an empty hash and non-null empty/false values. Fixed UTC
timestamps make the projection deterministic. The two removed rows cover
migration 127's eligibility rules: one has a nonempty hash and an existing
feed, while one has an empty hash. The expected tombstone list includes only
the former.

## Expected evidence

`fixture.Expected` is declared separately from the SQL and contains fixed
selected values plus independently specified counts for the source baseline,
target, source-version recovery restore, and broken-migration input. Counts
match the fields in `miniflux.CountsSQL`:

| Stage | Schema | Users | Categories | Feeds | Unread | Read | Removed | Starred | Tagged | Tombstones |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Baseline | 125 | 1 | 2 | 2 | 1 | 1 | 2 | 1 | 1 | not present |
| Target | 132 | 1 | 2 | 2 | 1 | 1 | 0 | 1 | 1 | 1 expected key |
| Recovery restore | 125 | 1 | 2 | 2 | 1 | 1 | 2 | 1 | 1 | not present |
| Broken-migration input | 125 | 1 | 2 | 2 | 1 | 0 | 3 | 0 | 0 | not present |

The normal run should seed only the baseline source database. The target and
recovery databases must each restore the unchanged staged backup independently
into fresh volumes. Target checks compare all-row projection and counts, then
compare the ordered eligible removed-key set with the tombstone-key set. The
empty-hash row is expected to be deleted without a tombstone. Recovery checks
compare against the original baseline; they must not use the target database.

The fixed IDs, titles, feed URLs, active-entry values, and eligible tombstone
key are available from `fixture.Expected`. The API checks can use these values
to confirm the expected synthetic user and representative category, feed,
unread, read, and starred rows. API pagination means those endpoint checks are
representative; database counts and the complete projection are the all-row
evidence.

## Isolated negative control

`fixture.BrokenMigrationSQL` exists only for the negative migration control.
Apply it only after restoring the original staged backup into a separate,
disposable negative-phase database and confirming that the fixed synthetic
read row is present. It changes exactly that row into a removed row whose
feed ID does not exist. The transaction temporarily sets
`session_replication_role` to `replica`, checks that exactly one row changed,
then explicitly restores `origin` before commit. PostgreSQL permits this
setting only to a superuser. If a precondition fails, the transaction fails;
it does not select a different row or invent IDs.

Run the pinned known-bad Miniflux 2.3.0 image only against this disposable
negative database and expect migration 127 to fail on the missing-feed
foreign key. The normal 2.3.3 target/recovery data, source backup, baseline,
and successful run never use this mutation. The product runner must call this
constant only from its explicit negative-control phase; it is not a generic
migration hook and must never be included in normal seed or upgrade SQL.

## Schema and qualification limits

The SQL follows the upstream [Miniflux 2.2.19 migration source](https://github.com/miniflux/v2/blob/2.2.19/internal/database/migrations.go): the initial `users`, `categories`, `feeds`, and `entries` definitions; the later `starred`, `tags`, `changed_at`, `created_at`, and `hide_globally` columns; and the source-version schema 125. Some fields permit SQL `NULL`. A prior fixture version put NULLs on the active unread row; the authenticated unread endpoint returned HTTP 500 because Miniflux's [entry query scan](https://github.com/miniflux/v2/blob/2.2.19/internal/storage/entry_query_builder.go) scans fields such as `comments_url` into Go strings. The current fixture keeps NULL edge cases on the eligible removed row and gives active rows values required by that API. The upstream [admin command](https://github.com/miniflux/v2/blob/2.2.19/internal/cli/create_admin.go) has separate environment-driven and interactive functions; the [user storage code](https://github.com/miniflux/v2/blob/2.2.19/internal/storage/user.go) inserts the `All` category during `CreateUser`. Migration 127's target behavior is in the upstream [Miniflux 2.3.3 migration source](https://github.com/miniflux/v2/blob/2.3.3/internal/database/migrations.go): it copies removed rows with a nonempty hash and an existing feed into tombstones, then deletes all removed rows. The [2.3.0 migration source](https://github.com/miniflux/v2/blob/2.3.0/internal/database/migrations.go) is the negative-control comparison and lacks the orphan-feed exclusion.

The opt-in engine test has passed against a fresh explicitly named disposable
WSL runtime. It executed source schema inspection, environment-based admin
creation, `SeedSQL`, baseline counts, the deterministic projection, an
authenticated unread API request with the expected synthetic entry ID, and
live phase-boundary verification. Separately, the complete three-phase CLI
rehearsal has passed on a disposable WSL2 runtime, including target migration,
target API/data checks, independent source-version recovery, and owned cleanup.
The v0.2.0 Windows and Linux packages also passed that workflow in the
environments recorded in their release verification. Ubuntu CI exercises a
separately built package. These are historical fixture results; they do not
qualify a different source revision, archive, or environment. Consult the
selected release's `verification.json` for exact artifact acceptance. The
negative-control migration failure is a separate qualification case. The
nullable fields on a removed row cover storage/projection edges; they do not
establish API behavior for SQL `NULL`.
