# Architecture and implementation contracts

Rehearse is a trusted local Go runner. It streams a supplied PostgreSQL custom-format backup into fresh Docker resources, starts a pinned application version, measures data and authenticated API behavior, and writes a local report. It never runs against an existing database or edits the source backup.

The first supported rehearsal is Miniflux 2.2.19 to 2.3.3 on PostgreSQL 17.11, Linux amd64 containers. Support for another pair requires adapter review and a real fixture test; accepting an arbitrary tag would bypass that contract. Forgejo is a later v1 task.

## Packages

- `internal/spec`: strict version-one user configuration, with environment variable names for authentication.
- `internal/state`: private installation identity, durable run intents, staged backup and local history. Run IDs are random lowercase 32-character hexadecimal values. Resources have deterministic names and owner/run/kind labels.
- `internal/engine`: fixed Compose generation, Docker capability/identity checks, bounded subprocess operations and live ownership inspection. No user Compose, image, mount, command or Docker arguments are accepted.
- `internal/miniflux`: pinned images, database projections, expected migration transformations and fixed API checks. It supplies observations, never deletes Docker resources.
- `internal/report`: version-one check results, safe JSON and escaped HTML. Each check is `passed`, `failed` or `not-run`; readiness alone cannot pass a rehearsal.
- `cmd/rehearse`: `plan`, `run`, `report`, `cleanup` and signal handling.

The runner creates a baseline environment, a separate target environment and a fresh old-version recovery environment from the same staged backup. The target is never downgraded. Each phase has a fresh volume, internal network and fixed database, migration, application and probe containers. No application ports are published. API probes run inside the phase network, with authentication passed over stdin rather than command arguments.

## Ownership and interruption

Persist the complete resource intent before asking Docker to create anything. Every planned resource includes its type, exact name and installation/run/kind labels; each run records the daemon identity. On cleanup, inspect live resources and compare those facts. Remove only verified objects, using immutable IDs where Docker provides them. Missing resources are already absent; foreign or mismatched objects are preserved and reported. Cleanup never runs a global prune or accepts an arbitrary Compose project.

The state directory belongs to the local account. This is not protection against a malicious host administrator or another actor with access to the Docker daemon. Restricting cleanup to verified owned resources protects against ordinary mistakes and interrupted runs.

## Network boundary

An `internal` bridge alone still exposes its bridge gateway. Rehearse requires isolated gateway mode and verifies that the requested network options actually took effect. All container attachments must belong to the phase network. There are no bind mounts, Docker socket mounts, privileged containers or host networking. Controlled positive and negative connectivity tests must demonstrate the advertised boundary on each supported runtime before release.

Runtime capability or data checks that were not executed remain unqualified. The host runner and Docker engine are trusted; this is not a sandbox for hostile container images.

## Private data

The staged backup and original application records can contain private data. Keep them locally in the managed run directory. Reports contain counts, SHA-256 fingerprints, fixed check IDs and safe error codes; they do not contain database rows, raw subprocess errors or resolved authentication values. Source backup hashes before and after the run prove only that the supplied file was unchanged during this rehearsal.
