# Forgejo adapter contract

The fixed implementation pair is Forgejo 15.0.9 to 16.0.5 with PostgreSQL
17.11 on Linux/amd64. The adapter does not accept caller-selected image names
or versions. The image references below are immutable platform-specific
image digests; the API probe reuses the curl 8.22.0 image pin already used by
the Miniflux adapter.

Full-fixture runtime qualification for this Forgejo pair is pending. Fixture
creation, startup, migration, baseline/target/recovery restore, API checks and
cleanup still need to pass together against the pinned images on each
supported runtime. This document records the implementation contract; it is
not a v1 qualification claim.

| Role | Immutable image reference |
| --- | --- |
| Source | `codeberg.org/forgejo/forgejo@sha256:46c82686a28aae45154325501ffa51aaee2cdf6cdfcf92137e3f7cce54987823` |
| Target | `codeberg.org/forgejo/forgejo@sha256:523de0217475297d05786d7551c1c1d6b5c8b90d6fee7189e88a234260ec0e74` |
| PostgreSQL | `docker.io/library/postgres@sha256:3cec7eb015ba8adb28139fa5c83b8489cdf0e666e53dfdf20f598ae0cc8739e3` |
| API probe | `docker.io/curlimages/curl@sha256:43366cd60f226c7655181a0f7e85c468a41d182fdd2dc2c1c3b872a2b9d05d7a` |

Forgejo 16.0.5's published support window ends on 29 October 2026, so the
fixed pair must be reviewed again before a release after that date.

## Offline backup format

The input is one ZIP file containing exactly these three regular entries in
this order: `manifest.json`, `database.pgdump`, and `forgejo-data.tar`. The
manifest binds the archive to format version 1, the fixed version/image pair,
and SHA-256 plus uncompressed size for the database and data TAR. It is
canonical JSON with no unknown fields. ZIP64, encryption, prefixes, comments,
entry extras, hidden members, gaps and trailing payload are rejected before
the ZIP reader builds its entry index. The archive is fully read and checked
before restore; its caller-owned open `ReaderAt` must remain open and
unchanged through restore.

`database.pgdump` is a PostgreSQL custom-format `pg_dump` archive and must
start with the `PGDMP` signature. `forgejo-data.tar` represents the complete
contents of the dedicated `/data` volume from the same stopped Forgejo
instance. Forgejo recommends a synchronized backup of the database and all
storage, and specifically cautions that SQL embedded in `forgejo dump` has
longstanding reinjection bugs. For this contract, stop the application before
producing the SQL dump and TAR, and do not write to either source while
`WriteArchive` runs.

The version-one archive limits are: 2 GiB for the complete ZIP, 32 KiB for
the manifest, 1 GiB each for the custom database dump and compressed TAR
member, 1 GiB expanded TAR contents, 256 MiB per file, 100,000 TAR entries,
and 4,096 UTF-8 bytes per path. Those bounds make this format suitable for
the small synthetic fixture; they are not a claim that arbitrary Forgejo
installations fit. `WriteArchive` accepts regular non-symlink source files,
keeps the same file descriptors open while inspecting and copying, verifies
their identity and contents, and returns only fixed safe error codes. A
failed write leaves a partial destination that must be discarded.

The `rehearse archive` CLI uses a temporary file in the output directory and
publishes it without overwriting by creating a hard link. The output filesystem
must support same-directory hard links (tested on NTFS and ext4); unsupported
publication fails with `ARCHIVE_WRITE_FAILED`. The CLI removes its own partial
file on failure and does not fall back to an overwrite-prone copy.

The TAR validator accepts regular files and directories only. It rejects
absolute or traversal paths, internal `.` segments, backslashes, duplicate
or conflicting paths, Windows device names, the reserved top-level
`.rehearse-runtime` directory, symlinks, hard links, devices, FIFOs, special
permission bits, unexpected owners, unsupported PAX attributes, nonzero data
after the end markers, and size/count overflows. A single leading `./` and a
root `.` directory entry are normalized for standard TAR output. Accepted
owners are UID/GID 0 or 1000 and accepted modes contain only ordinary 0777
permission bits.

The runner must restore the same staged archive into fresh, independently
owned database and data volumes for baseline, upgraded target and recovery.
The target is migrated once; recovery always uses the unchanged original dump
and TAR with the old image, never a downgrade of the target. The old archived
`app.ini` is data, not trusted runtime configuration: the runner must generate
its own fixed config and keep it under the reserved `.rehearse-runtime`
path. The archive is never restored to an existing user volume or production
endpoint.

## Database observations

`SchemaSQL` checks the three upstream migration trackers. Both tags record
Gitea schema version 305 in `version(id=1)` and Forgejo legacy migration
version 44 in `forgejo_version(id=1)`. The modern table is singular,
`forgejo_migration`; source 15.0.9 registers 28 exact migration IDs and target
16.0.5 registers 39. The adapter compares the sorted complete ID lists, not
just their counts. It excludes `created_unix`, which is a migration event
timestamp rather than schema state.

`ProjectionSQL` emits one JSON row per global user and repository, sorted by
record kind and numeric primary key, with SHA-256 streamed from the SQL
output. The common user fields are `id`, `lower_name`, `name`, `is_active`
and `is_admin`. The common repository fields are `id`, `owner_id`,
`lower_name`, `name`, `default_branch`, `is_private`, `is_empty`,
`is_archived`, `is_mirror`, `is_fork`, `fork_id`, `is_template`,
`template_id` and `object_format_name`. There are no joins, so an orphan row
cannot disappear from the projection. Passwords, private email, timestamps,
counters, repository size caches and other volatile values are excluded.
`CountsSQL` reports bounded global counts for users, repositories and
repository state flags.

The current profile requires at least one global user and repository and
rejects mirror repositories. The token's authenticated API view must include
at least one non-empty repository with an eligible content sample; a view
with no verified content fails the phase's API check. Every database repository must have a detected
bare Git root in the data snapshot. Empty installations and mirrors are
outside this profile; they do not produce a passing rehearsal.

The all-user and all-repository SQL projection is the global identity check.
The authenticated `/user/repos` API view is limited by Forgejo's visibility
rules and pagination, so it is an additional behavior check rather than a
replacement for that SQL comparison. The API checks authenticate as the
synthetic fixture user, confirm the running version and identity, list the
fixture repository, inspect its default branch and compare selected
synthetic file contents. Response bodies and file contents must never enter
reports or logs.

## Git repository-file projection

For every detected bare Git repository root in the `/data` TAR, the adapter
streams a digest over the relative member path, file size and SHA-256 of each
included file. A root is detected when a TAR path has an ancestor directory
component ending in `.git` (case-insensitive); it must contain a regular
non-empty `HEAD` file. The projection compares all detected roots and included files
between baseline, target and recovery, and its repository count must match the
database repository count.

The projection excludes directory metadata and TAR ordering, the reserved
top-level `.rehearse-runtime` namespace (matched case-insensitively), each
repository root's exact `description` file, files under `hooks/` whose content contains the exact
`AUTO GENERATED BY GITEA` marker, and `hooks/` paths ending in `.sample`
(case-insensitive). Every other file under a detected Git root participates.
This is a Git repository-file fingerprint, not a fingerprint of all `/data`:
files outside detected Git roots, Git object-graph consistency and application
data stored elsewhere are outside it.

## Authenticated API contract

The adapter constructs only fixed-origin `GET` requests to `http://app:3000`
inside the private phase network. Its endpoint helpers encode user, repository,
branch, file path and ref values as path/query components and reject traversal
or non-allowlisted endpoints. The allowed shapes are:

- `/api/v1/version` and `/api/v1/user`;
- `/api/v1/user/repos?limit=50&page=N` for canonical page numbers;
- `/api/v1/repos/{owner}/{repo}`;
- `/api/v1/repos/{owner}/{repo}/branches/{branch}`;
- `/api/v1/repos/{owner}/{repo}/git/trees/{commitSHA}?recursive=true&page=N&per_page=1000`;
- `/api/v1/repos/{owner}/{repo}/contents/{path}?ref={ref}`.

Authentication uses a synthetic access token through Forgejo's documented
`Authorization: token …` header. Curl receives its configuration on stdin
with `--disable --config -`; it disables proxy use and redirects and applies
fixed connection, total-time and response-size limits. No host port is
published. Parsers bound response size, require the expected JSON shape,
reject duplicate JSON object keys and duplicate repository IDs, and return
fixed safe errors. File content access is bounded to 1 MiB and exposed only
through a defensive-copy method; formatted/JSON output omits the content.

For each non-empty API-visible repository, the runner requests every
recursive tree page using the default branch's immutable commit SHA. Pages are
fixed at 1,000 entries; the contract accepts at most 10,000 total entries
(10 pages) and fails closed above that limit. Page lengths must match
`total_count`, every page must report the same resolved tree object ID and
total, and paths must be unique across the complete listing. Forgejo's
response `sha` is the resolved tree object ID, which can differ from the
commit SHA used in the request.

In the pinned Forgejo source, `truncated` is true whenever `total_count` is
greater than `per_page`, including on the last page. The runner validates
that source behavior but uses the expected page count and exact page lengths
to determine completeness; it does not treat `truncated` as a per-page
"fetch another page" signal. This differs from the wording in the upstream
Swagger comment.

After all tree pages pass validation, the runner sorts eligible files by path
and selects up to the first three regular or executable blobs no larger than
1 MiB (`100644` and `100755` modes). Each non-empty repository must have at
least one eligible file or the API check fails. Directories, symlinks,
submodules and larger blobs are not content-sampled. For each selected path,
the runner requests `/contents/{path}` with `ref` set to the immutable branch
commit SHA, verifies the decoded byte count and recomputes the Git blob object
ID using the repository's SHA-1 or SHA-256 object format. It also compares a
SHA-256 digest of the captured content between baseline, target and recovery.
The tree-entry URL is never fetched. These samples establish behavior for
the selected files only.

The API checks demonstrate the fixture's selected behavior and files, not
every repository object, branch, Git blob, issue, pull request, package,
attachment, LFS object, action run, wiki or external storage location. This
v1 adapter contract has no general application-data equivalence claim beyond
its SQL projection, fixed synthetic API checks and recovery checks.

## Upstream references

- [Forgejo 15.x releases](https://forgejo.org/releases/15.x/) and [tagged v15.0.9 migration source tree](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.9/models/).
- [Forgejo 16.x releases](https://forgejo.org/releases/16.x/) and [tagged v16.0.5 migration source tree](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/models/).
- [Forgejo Docker installation](https://forgejo.org/docs/v16.0/admin/installation/docker/) — official `/data` volume and UID/GID guidance.
- [Forgejo upgrade guide](https://forgejo.org/docs/v16.0/admin/upgrade/) — synchronized storage/DB backups, SQL reinjection warning, and 16.x cleanup notes.
- [Forgejo API usage](https://forgejo.org/docs/v16.0/user/api/usage/) — access-token authentication and API usage/pagination.
- [Forgejo 16.0.5 tree route](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/routers/api/v1/repo/tree.go), [tree pagination implementation](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/services/repository/files/tree.go), and [tree response schema](https://codeberg.org/forgejo/forgejo/src/tag/v16.0.5/modules/structs/repo_tree.go) — request parameters, resolved tree ID, response fields and pinned pagination behavior.
- [Forgejo 15.0.9 tree route](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.9/routers/api/v1/repo/tree.go) and [tree pagination implementation](https://codeberg.org/forgejo/forgejo/src/tag/v15.0.9/services/repository/files/tree.go) — source-tag comparison for the same API behavior.
- [Forgejo v16.0 release announcement](https://forgejo.org/2026-07-release-v16-0/) — breaking changes and release-specific notes.
- [Forgejo release lifecycle](https://forgejo.org/releases/) — current release and support windows.
- [Official Forgejo container registry](https://codeberg.org/forgejo/-/packages/container/forgejo) — image tags corresponding to the pinned image artifacts.
