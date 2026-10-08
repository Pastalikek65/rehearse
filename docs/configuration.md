# Configuration

Rehearse accepts one versioned JSON configuration format. The MVP supports the Miniflux adapter with Miniflux source version `2.2.19`, target version `2.3.3`, and PostgreSQL version `17.11` only. The negative-control target `2.3.0` is reserved for failure-fixture testing and is rejected by the normal configuration parser.

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
| `adapter` | Required adapter identifier. Currently `miniflux`. |
| `sourceVersion` | Required canonical stable SemVer for the old Miniflux version. |
| `targetVersion` | Required canonical stable SemVer for the requested Miniflux version. |
| `postgresVersion` | Required PostgreSQL version. Currently `17.11`. |
| `backupPath` | Required, nonblank path to the supplied backup. The runner checks that it resolves to a readable regular file before use. |
| `authEnvRefs` | Required object containing either only an `apiToken` environment-variable name or exactly both `username` and `password` environment-variable names. |

For `sourceVersion` and `targetVersion`, canonical stable SemVer uses three numeric components without a `v` prefix, leading zeroes, prerelease, or build metadata. Syntactically valid versions still need to be in the adapter's supported version-pair list; the MVP accepts only `2.2.19` → `2.3.3`.

Authentication references use uppercase environment-variable identifiers: the first character must be `A`–`Z` or `_`; later characters may also include digits. The parser validates the reference syntax but does not read environment values. Do not put passwords or API tokens in the JSON file. The runner resolves the selected variables at execution time. An authentication object cannot combine modes or include an unused blank key.

The parser rejects input larger than 64 KiB, invalid or trailing JSON, duplicate keys, `null` values, unknown fields, wrong field types, unsupported schema/adapter/version selections, and invalid authentication-reference combinations. Errors are fixed safe codes; they do not include the source JSON or credential text. The parser does not inspect the backup path or start Docker. File validation and secret resolution belong to the run preflight.

The backup must be a PostgreSQL custom-format archive (for example, produced by `pg_dump --format=custom`). A `.sql` text dump is unsupported. The offline plan recognizes only the archive header; full inspection and restore happen during `run`. Backup paths in a CLI configuration are resolved relative to that configuration file.

This format deliberately has no fields for Compose documents, image names, commands, URLs, port mappings, or mounts. Runtime services and image references come from the adapter's reviewed implementation and version manifest.
