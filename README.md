# Rehearse

[![CI](https://github.com/Pastalikek65/rehearse/actions/workflows/ci.yml/badge.svg)](https://github.com/Pastalikek65/rehearse/actions/workflows/ci.yml)

Test a self-hosted application upgrade from a backup, compare its data, and prove that the old backup still restores.

Rehearse is a local Go CLI for maintainers who want evidence before an upgrade. It restores your supplied backup into three fresh environments: the current application, the upgraded application, and a clean recovery instance of the old application. It writes a JSON/HTML report and removes only resources it can prove it owns.

**Public v0.1 support:** Miniflux **2.2.19 → 2.3.3** with PostgreSQL **17.11**, using pinned Linux amd64 images. Windows requires an explicitly selected WSL2 distribution. See [support and limitations](docs/support.md) before using your own backup. The current development checkout also contains a Forgejo adapter and archive/history/recovery commands, but the Forgejo full-fixture qualification is pending; these development features are not part of the immutable v0.1 release.

## Install

Portable archives are published on the [releases page](https://github.com/Pastalikek65/rehearse/releases). Extract the archive for your platform and run `./rehearse --help` on Linux or `.\rehearse.exe --help` in Windows PowerShell. Archives are unsigned; compare their SHA-256 values with the release checksums. Docker and Compose remain external prerequisites.

The [0.1 MVP preview](https://github.com/Pastalikek65/rehearse/releases/tag/v0.1.0) is Miniflux-only and includes an actual synthetic [HTML result](https://github.com/Pastalikek65/rehearse/releases/download/v0.1.0/example-report.html), JSON result and archive verification evidence. A passing result records 23 separate checks, including old-backup recovery; read the scope in [support](docs/support.md).

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
the development Forgejo implementation also validates its ZIP envelope,
member hashes and data TAR. Neither proves PostgreSQL restore validity or
application behavior. `run` inspects and restores the staged archive, checks
real authenticated API responses, compares retained data and the expected
removed-entry conversion, and restores the original backup into a fresh
old-version environment. Container health alone cannot produce a passing result.

A failed run exits nonzero and marks dependent checks as `not-run`. Resources are normally cleaned even after failure. For a terminal run with held cleanup, read the saved result with `report --format json RUN_ID`, resolve the reported ownership issue, then run `cleanup [--wsl-distro NAME] RUN_ID`. If an existing process lock blocks the command, follow the manual diagnosis in [state-storage](docs/state-storage.md); reports and cleanup fail closed while that lock remains. Never globally prune Docker to recover a rehearsal.

## Use your own backup

Copy the [example configuration](examples/miniflux/rehearse.json), select a PostgreSQL **custom-format** backup, and supply the existing Miniflux account's API token or username/password through environment references. Backup paths are relative to the configuration file. SQL text dumps and arbitrary version pairs are rejected. [Configuration reference](docs/configuration.md).

The source backup is read, hashed, and copied; it is never deliberately modified. Local state contains a copy of its data and its source path. Treat the per-user Rehearse directory as private. Reports contain digests, counts and fixed check codes, without credentials or business records.

## Development-only Forgejo commands

These commands are present in the current source checkout; they are not included in the public v0.1 package, and Forgejo remains pending full-fixture qualification. See the [Forgejo adapter contract](docs/forgejo-adapter.md) and [history and recovery guide](docs/history-recovery.md).

```sh
./bin/rehearse archive --database <database.pgdump> --data <forgejo-data.tar> --output <new.zip>
./bin/rehearse history [--limit N] [--json]
./bin/rehearse recover <run-id>
```

`archive` only packages operator-supplied files; it does not contact Docker or prove that the database and data TAR form one consistent snapshot. Stop Forgejo before capturing both files. Keep the resulting archive private. A development Forgejo configuration may point `backupPath` at this ZIP, but parsing the config does not mean the adapter is qualified or supported for a release.

## Boundaries and project status

Every phase uses fresh named volumes and an internal isolated network. Application ports, production volumes, arbitrary host mounts, Docker sockets inside containers, host networking and privileged containers are prohibited. The host CLI itself needs trusted access to Docker; Rehearse is not a sandbox for hostile containers. Read [SECURITY.md](SECURITY.md).

- [Support matrix and known limitations](docs/support.md)
- [Architecture](docs/architecture.md) and [rehearsal checks](docs/rehearsal-flow.md)
- [State and interruption recovery](docs/state-storage.md)
- [Forgejo adapter contract (development; qualification pending)](docs/forgejo-adapter.md) and [run history and recovery](docs/history-recovery.md)
- [Roadmap](docs/roadmap.md)
- [Türkçe hızlı başlangıç](docs/quickstart.tr.md)
- [Contributing](CONTRIBUTING.md) and [third-party inventory](THIRD_PARTY.md)

Licensed under [Apache-2.0](LICENSE).
