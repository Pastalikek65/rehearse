# Roadmap

## Supported scope

Rehearse supports the fixed Miniflux 2.2.19 → 2.3.3 and Forgejo 15.0.9 →
16.0.5 pairs on PostgreSQL 17.11. Other adapters and version pairs require a
separate contract, fixture, and qualification. See [support](support.md) for
current boundaries and the selected release's `verification.json` for exact
artifact results.

## Reliability follow-up

The known P2 Windows state/staging reliability issue and operator guidance are
tracked in [support](support.md). Its historical causes remain unknown; the
controlled file-share denial test is not presented as their cause. Follow-up
work should collect safe diagnostics if the original failure recurs and keep
the failure path fail-closed.

Network acceptance is specific to the runtime, endpoints, address families,
and protocols listed in each release's verification record. Those results do
not imply universal isolation across other runtimes or network paths.

## Future work

- Recheck the fixed Forgejo pair before a release after its documented support
  window ends on 29 October 2026.
- Add an adapter or version pair only after its schema, data transformations,
  API checks, recovery profile, and cleanup ownership have a tested contract.
- Consider broader physical-Linux coverage and additional controlled network
  paths as separate qualification work.

These items describe possible follow-up, not compatibility or release-date
promises.
