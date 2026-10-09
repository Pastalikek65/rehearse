# Synthetic Forgejo rehearsal

Use this example to rehearse **Forgejo 15.0.9 → 16.0.5** with PostgreSQL
17.11. It contains one synthetic user, one private repository on `main`, and
three UTF-8 files. All records and the scoped read token are intentionally
public. They belong only to disposable instances restored from this archive.
The independently authored synthetic records and file content are covered by
the repository's Apache-2.0 license. The data archive also preserves generated
Forgejo hook scripts and Git sample hook templates under their upstream
licenses and notices. See [the third-party inventory](../../THIRD_PARTY.md)
and [fixture licensing metadata](fixture.json) for the embedded paths and
license texts; the archive as a whole is not described by one license.

The fixed Forgejo 15.0.9 → 16.0.5 adapter is supported. Check the
[releases page](https://github.com/Pastalikek65/rehearse/releases) for the
current package and its `verification.json`; that record identifies which
exact archive and environments were accepted. Earlier v0.1.0 and v0.2.0
package results are historical and do not qualify a different build. See the
[support matrix](../../docs/support.md) for the fixed support contract.

From the repository root after building `bin/rehearse`:

```sh
export REHEARSE_FORGEJO_EXAMPLE_TOKEN="$(cat examples/forgejo/fixture-read-token.txt)"
./bin/rehearse plan examples/forgejo/rehearse.json
./bin/rehearse run examples/forgejo/rehearse.json
```

Windows PowerShell (name your prepared disposable WSL2 distribution):

```powershell
$env:REHEARSE_FORGEJO_EXAMPLE_TOKEN = (Get-Content -Raw examples/forgejo/fixture-read-token.txt).Trim()
.\bin\rehearse.exe plan examples/forgejo/rehearse.json
.\bin\rehearse.exe run --wsl-distro MyRehearsalWSL examples/forgejo/rehearse.json
```

Use `./bin/rehearse` after a source build, or `./rehearse` in the extracted
Linux package (`.\bin\rehearse.exe` or `.\rehearse.exe` in Windows PowerShell).
The runner uses fresh volumes and private networks and publishes no app port.
It tests the baseline, migrates a separate target, and restores the original
backup into a fresh old-version recovery instance. The Forgejo report contains
checks, SQL/Git fingerprints, and API content observations. Consult the
release verification record for the acceptance result of a specific package.
Print a report with `rehearse report --format json RUN_ID` or
`rehearse report --format html RUN_ID`. Replace the executable path above with
`./rehearse` or `.\rehearse.exe` when this example is shipped in a package.

The original Forgejo application was stopped before both database and data
were captured. The temporary write token was revoked before capture. The
archive excludes the runner's generated configuration; the original synthetic
config is preserved as data and is not executed. The token file contains the
final read-only token for the synthetic account (`read:user,read:repository`).
An offline `plan` validates the envelope, hashes and data TAR; successful
database restore and application behavior require `run`.

`fixture.json` records the archive identity and source-workflow evidence.
The archive is 78,904 bytes with SHA-256
`b165b79a91d96f422b494d01b222c602be19345951ef4069acad8cf737237905`.
See the [adapter contract](../../docs/forgejo-adapter.md) for exactly which
database fields, Git files and API operations the comparison covers.
