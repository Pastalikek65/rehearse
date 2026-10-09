# Roadmap

## Immutable v0.1 preview

The public v0.1.0 preview supports Miniflux 2.2.19 → 2.3.3 with PostgreSQL 17.11. Its package contains three independent restore environments, fixed API and data checks, private local state, JSON/HTML reports, and ownership-checked cleanup. It does not include Forgejo, archive creation, run history, or dead-process recovery. Its published artifacts remain unchanged.

## Released v0.2.0 beta

The [v0.2.0 beta](https://github.com/Pastalikek65/rehearse/releases/tag/v0.2.0) adds Forgejo 15.0.9 → 16.0.5, archive/history/recovery commands, and a synthetic Forgejo example. Both application pairs use PostgreSQL 17.11 and pinned Linux amd64 images. Exact Windows and Linux packages passed both bundled adapter rehearsals and their platform-specific interruption/recovery checks; see [verification.json](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/verification.json).

Three historic intermittent Windows state/staging test failures remain unresolved. Their original causes were not captured, and successful repeats plus a separate controlled file-share-denial test do not establish a fix. The separate native-Linux controlled egress-canary qualification also remains open. See [support and limitations](support.md) for the operator guidance and evidence scope.

## Production v1

- Resolve the open Windows storage reliability finding or obtain sufficient diagnostic evidence to assess it.
- Complete and review the native-Linux controlled egress qualification.
- Finish independent product and security review, close critical/high findings, then build and accept exact v1 packages for Windows and Linux.
- Recheck the fixed Forgejo version pair before a release after its documented support window ends on 29 October 2026.

Additional adapters and arbitrary application versions require separate tested contracts. The beta does not imply their compatibility. See the [Forgejo adapter contract](forgejo-adapter.md) and [history/recovery behavior](history-recovery.md).
