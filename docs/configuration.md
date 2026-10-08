# Configuration

Rehearse uses one versioned JSON configuration format. The immutable public v0.1 release supports only Miniflux `2.2.19` → `2.3.3` with PostgreSQL `17.11`. The current development source also parses the fixed Forgejo `15.0.9` → `16.0.5` pair, but its full-fixture qualification is pending; it is not public v0.1 support. The Miniflux negative-control target `2.3.0` is reserved for failure-fixture testing and is rejected by normal configuration parsing.

```json
{
  "schemaVersion": 1,
  "adapter": "miniflux",
  "sourceVersion": "2.2.19",
  "targetVersion": "2.3.3",
  "postgresVersion": "17.11",
  "backupPath": "./miniflux.dump",
  "authEnvRefs": {
    "apiToken": "MINIFLUX_API_TOKEN"
  }
}
```

The configuration has these fields:

| Field | Meaning |
| --- | --- |
| `schemaVersion` | Required integer. Currently `1`. |
| `adapter` | Required adapter identifier. Public v0.1 accepts `miniflux`; development source also recognizes `forgejo` pending qualification. |
| `sourceVersion` | Required canonical stable SemVer for the selected adapter's old version. |
| `targetVersion` | Required canonical stable SemVer for the selected adapter's target version. |
| `postgresVersion` | Required PostgreSQL version. Both fixed adapter pairs use `17.11`. |
| `backupPath` | Required, nonblank path to the adapter's supplied backup. Miniflux uses a PostgreSQL custom-format dump; development Forgejo uses the offline ZIP described below. |
| `authEnvRefs` | Required object containing either only an `apiToken` environment-variable name or exactly both `username` and `password` environment-variable names. Development Forgejo requires an API token. |

For `sourceVersion` and `targetVersion`, canonical stable SemVer uses three numeric components without a `v` prefix, leading zeroes, prerelease, or build metadata. Syntactically valid versions still need to match the fixed version pair for the selected adapter. Public v0.1 supports only Miniflux `2.2.19` → `2.3.3`.

Authentication references use uppercase environment-variable identifiers: the first character must be `A`–`Z` or `_`; later characters may also include digits. The parser validates the reference syntax but does not read environment values. Do not put passwords or API tokens in the JSON file. The runner resolves the selected variables at execution time. An authentication object cannot combine modes or include an unused blank key.

The parser rejects input larger than 64 KiB, invalid or trailing JSON, duplicate keys, `null` values, unknown fields, wrong field types, unsupported schema/adapter/version selections, and invalid authentication-reference combinations. Errors are fixed safe codes; they do not include the source JSON or credential text. The parser does not inspect the backup path or start Docker. File validation and secret resolution belong to the run preflight.

For Miniflux, the backup must be a PostgreSQL custom-format archive (for example, produced by `pg_dump --format=custom`); a `.sql` text dump is unsupported. The offline plan recognizes only its archive header; full inspection and restore happen during `run`. Backup paths in a CLI configuration are resolved relative to that configuration file.

## Development-only Forgejo configuration

The current development parser accepts this fixed pair, but full-fixture runtime qualification has not passed. This is not a supported public v0.1 configuration. The path is an operator-provided offline archive; this example does not imply that a ready-made Forgejo fixture is available.

```json
{
  "schemaVersion": 1,
  "adapter": "forgejo",
  "sourceVersion": "15.0.9",
  "targetVersion": "16.0.5",
  "postgresVersion": "17.11",
  "backupPath": "./forgejo-offline.zip",
  "authEnvRefs": {
    "apiToken": "FORGEJO_API_TOKEN"
  }
}
```

The ZIP contains `manifest.json`, `database.pgdump`, and `forgejo-data.tar` as specified in the [Forgejo adapter contract](forgejo-adapter.md). The development `archive` command can package operator-provided inputs, but it does not prove cross-file consistency; stop Forgejo before capturing the database and data TAR. Treat the archive as private instance data. See [run history and recovery](history-recovery.md) for the development-only history/recovery commands.

This format deliberately has no fields for Compose documents, image names, commands, URLs, port mappings, or mounts. Runtime services and image references come from the adapter's reviewed implementation and version manifest.
