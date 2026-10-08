# Synthetic Miniflux rehearsal

`miniflux-source-125.dump` is a PostgreSQL custom-format archive generated from a fresh Miniflux 2.2.19 database (schema 125) on PostgreSQL 17.11. Its records come from the [fixture source](https://github.com/Pastalikek65/rehearse/blob/main/internal/fixture/fixture.go). It contains no imported user data.

| Property | Value |
| --- | --- |
| Bytes | 51,897 |
| SHA-256 | `27073679601eadc2e414bccac63f0a29ab517cc496de76e260890b909f856c14` |
| Public username | `rehearse-fixture` |
| Public password | `synthetic-fixture-password` |
| Expected baseline | One user, two categories, two feeds, one unread entry, one read entry, two removed entries, one starred entry and one tagged entry |

The target migration removes the removed entries and creates one eligible tombstone. Recovery restores the original two removed entries in a fresh old-version database. Unicode, line breaks, fixed timestamps and nullable removed-entry fields exercise the projection checks.

Follow the [root quickstart](../../README.md#get-your-first-result). Credentials here are public test credentials and should never be reused in a real application.

Fixture generation is an explicit opt-in integration test: `TestMinifluxFixtureOnExplicitDisposableWSLRuntime`, with `REHEARSE_TEST_WSL` selecting a prepared disposable distribution and `REHEARSE_FIXTURE_OUTPUT` selecting a new output file. The test refuses to overwrite an existing output. The dump contains generated database structures and synthetic data; see [THIRD_PARTY.md](../../THIRD_PARTY.md) for upstream licenses.
