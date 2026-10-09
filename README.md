# Rehearse

[![CI](https://github.com/Pastalikek65/rehearse/actions/workflows/ci.yml/badge.svg)](https://github.com/Pastalikek65/rehearse/actions/workflows/ci.yml)

Test a self-hosted application upgrade from a backup, compare its data, and prove that the old backup still restores.

Rehearse is a local Go CLI for maintainers who want evidence before an upgrade. It restores your supplied backup into three fresh environments: the current application, the upgraded application, and a clean recovery instance of the old application. It writes a JSON/HTML report and removes only resources it can prove it owns.

**Supported pairs:** Miniflux **2.2.19 → 2.3.3** and Forgejo
**15.0.9 → 16.0.5**, both with PostgreSQL **17.11** and pinned Linux amd64
images. Only these fixed pairs are supported. Windows requires an explicitly
selected WSL2 distribution. See [support](docs/support.md) before using your
own backup.

See a real result: [Miniflux HTML report](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/miniflux-example-report.html) and [Forgejo HTML report](https://github.com/Pastalikek65/rehearse/releases/download/v0.2.0/forgejo-example-report.html), generated from the bundled synthetic examples with v0.2.0. Each new release records its own package acceptance separately.

## Install

Download the Windows x64 ZIP or Linux x64 tar.gz from [GitHub Releases](https://github.com/Pastalikek65/rehearse/releases). Extract it, then run `./rehearse --help` on Linux or `.\rehearse.exe --help` in Windows PowerShell. Archives are unsigned. Compare the archive with its release's `SHA256SUMS` and inspect the artifact-specific `verification.json`. The [v1.0.0 verification record](https://github.com/Pastalikek65/rehearse/releases/download/v1.0.0/verification.json) is the acceptance source for those exact archives and environments; only a matching artifact with a passed status is accepted. Docker and Compose remain external prerequisites.

Each release's verification record is separate from immutable `BUILD.json` package metadata. The record identifies exact archive digests and the acceptance results actually completed for that release; building an archive or seeing `packageAcceptance: not-run` in `BUILD.json` does not establish runtime acceptance. Earlier release contents and support scopes remain documented in their own release records.

Build from this checkout with Go 1.27:

```sh
mkdir -p bin
go build -trimpath -o bin/rehearse ./cmd/rehearse
./bin/rehearse --help
```

Windows PowerShell:

```powershell
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin/rehearse.exe ./cmd/rehearse
.\bin\rehearse.exe --help
```

Running a rehearsal requires Docker Engine 28+ and Docker Compose, with isolated dual-stack bridge support. On Linux, the CLI uses the local Unix socket at `/var/run/docker.sock`. On Windows, Docker Engine and Compose must be installed **inside the WSL2 distribution you name**. Rehearse does not install or start Docker for you.

The first run downloads four pinned images. Allow at least 4 GiB of available memory and space for the images, a staged backup, and three restored databases. Backup compression makes disk expansion unpredictable.

## Get your first result

The commands below use a source build in `bin/`. In an extracted portable archive, use `./rehearse` on Linux or `.\rehearse.exe` on Windows; the example files are in the same relative location.

The [synthetic example](examples/miniflux/README.md) includes a real PostgreSQL custom-format archive. Its credentials and records are deliberately public; it contains no user data.

Linux:

```sh
export REHEARSE_EXAMPLE_USER=rehearse-fixture
export REHEARSE_EXAMPLE_PASSWORD=synthetic-fixture-password
./bin/rehearse plan examples/miniflux/rehearse.json
./bin/rehearse run examples/miniflux/rehearse.json
```

Windows PowerShell (replace `MyRehearsalWSL` with your prepared distribution):

```powershell
$env:REHEARSE_EXAMPLE_USER = 'rehearse-fixture'
$env:REHEARSE_EXAMPLE_PASSWORD = 'synthetic-fixture-password'
.\bin\rehearse.exe plan examples/miniflux/rehearse.json
.\bin\rehearse.exe run --wsl-distro MyRehearsalWSL examples/miniflux/rehearse.json
```

The run prints its ID and report location. To read a saved result:

```sh
./bin/rehearse report --format json RUN_ID
./bin/rehearse report --format html RUN_ID
```

Replace `RUN_ID` with the ID printed by `run`. On Windows, use

```powershell
.\bin\rehearse.exe report --format json RUN_ID
.\bin\rehearse.exe report --format html RUN_ID
```

`plan` is offline. For Miniflux it reads the configuration and backup header;
for Forgejo it validates the ZIP envelope, member hashes and data TAR. Neither
proves PostgreSQL restore validity or application behavior. `run` inspects and
restores the staged archive, performs adapter-specific API and data checks, and
restores the original backup into a fresh old-version environment. Container
health alone cannot produce a passing result.

A failed run exits nonzero and marks dependent checks as `not-run`. Resources are normally cleaned even after failure. For a terminal run with held cleanup, read the saved result with `report --format json RUN_ID`, resolve the reported ownership issue, then run `cleanup [--wsl-distro NAME] RUN_ID`. If an existing process lock blocks the command, follow the manual diagnosis in [state-storage](docs/state-storage.md); reports and cleanup fail closed while that lock remains. Never globally prune Docker to recover a rehearsal.

## Use your own backup

Copy the [example configuration](examples/miniflux/rehearse.json), select a PostgreSQL **custom-format** backup, and supply the existing Miniflux account's API token or username/password through environment references. Backup paths are relative to the configuration file. SQL text dumps and arbitrary version pairs are rejected. [Configuration reference](docs/configuration.md).

The source backup is read, hashed, and copied; it is never deliberately modified. Local state contains a copy of its data and its source path. Treat the per-user Rehearse directory as private. Reports contain digests, counts and fixed check codes, without credentials or business records.

## Forgejo, archive, history, and recovery commands

The supported Forgejo adapter and these commands use the fixed Forgejo 15.0.9 → 16.0.5 pair. See the [synthetic Forgejo example](examples/forgejo/README.md), [Forgejo adapter contract](docs/forgejo-adapter.md), and [history and recovery guide](docs/history-recovery.md). Check the selected release's verification record for exact artifact acceptance.

```sh
./bin/rehearse archive --database <database.pgdump> --data <forgejo-data.tar> --output <new.zip>
./bin/rehearse history [--limit N] [--json]
./bin/rehearse recover <run-id>
```

`archive` only packages operator-supplied files; it does not contact Docker or prove that the database and data TAR form one consistent snapshot. Stop Forgejo before capturing both files. Keep the resulting archive private. Only the fixed pair described in the adapter contract is supported; parsing a different configuration does not imply support for other versions.

## Boundaries and project status

Every phase uses fresh named volumes and an internal isolated network. Application ports, production volumes, arbitrary host mounts, Docker sockets inside containers, host networking and privileged containers are prohibited. The host CLI itself needs trusted access to Docker; Rehearse is not a sandbox for hostile containers. Read [SECURITY.md](SECURITY.md).

- [Support matrix and known limitations](docs/support.md)
- [Architecture](docs/architecture.md) and [rehearsal checks](docs/rehearsal-flow.md)
- [State and interruption recovery](docs/state-storage.md)
- [Forgejo adapter contract](docs/forgejo-adapter.md) and [run history and recovery](docs/history-recovery.md)
- [Measured performance and scope](docs/performance.md)
- [Roadmap](docs/roadmap.md)
- [Türkçe hızlı başlangıç](docs/quickstart.tr.md)
- [Contributing](CONTRIBUTING.md) and [third-party inventory](THIRD_PARTY.md)

Licensed under [Apache-2.0](LICENSE).
