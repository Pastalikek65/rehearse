# Security

## Report a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/Pastalikek65/rehearse/security/advisories/new). Include the version/commit, platform, a synthetic reproduction and the affected boundary. Do not attach a real backup, credential, private report directory or business records to a public issue.

The main branch and current stable release line receive security fixes. Older preview and beta artifacts are superseded unless a release notice explicitly says otherwise. Use the [releases page](https://github.com/Pastalikek65/rehearse/releases) and [support matrix](docs/support.md) to identify the current version and its supported environments. The exact artifact and environment results are recorded in that release's `verification.json`.

## Trust and data boundaries

The CLI is a trusted local tool with access to Docker. Docker access itself is powerful. Rehearse is not a security sandbox for malicious containers, malicious SQL archives or a compromised host account. Use backups you trust and a separate rehearsal host or engine when isolation from other local workloads matters.

The reviewed adapter supplies fixed image digests, commands, service names and read-only API endpoints. User configuration cannot supply a Compose file, image, command, host mount or URL. The rehearsal containers do not receive the Docker socket, production volumes, host networking, published application ports or privileged mode.

Each phase has fresh managed volumes and an internal isolated bridge. The implementation checks the actual network, volume and container configuration. Cleanup checks the original daemon identity and exact run/owner/kind labels before removing a resource. It declines conflicting resources and never globally prunes Docker.

Credentials are resolved from environment-variable references, passed to the probe through stdin, and omitted from reports. Container/SQL/API diagnostics are reduced to fixed safe codes. Reports expose verification counts, digests and run identifiers; the private state directory contains the source path and a staged backup and must be treated as sensitive.

An abnormal process termination can leave resources and an ownership lock. Read [state-storage.md](docs/state-storage.md) before intervening. A lock that might belong to a live process must not be removed. Power-loss durability on Windows and universal protocol isolation are not claimed.
