# Contributing

Useful contributions include a synthetic reproduction of an adapter failure, a clearer setup instruction, or a reviewed adapter contract for a particular application/version pair. Open a focused issue before a large feature. Use private reporting for security defects.

## Develop

Install Go 1.27, then run:

```sh
go test -count=1 ./...
go vet ./...
go build ./cmd/rehearse
```

There are no third-party Go module dependencies. On Linux, also run `go test -race -count=1 ./...` with a supported C toolchain. Opt-in runtime tests are skipped in an ordinary unit suite; a skip is not a successful runtime qualification.

For the actual three-phase rehearsal, use only a prepared **disposable** Docker engine and the repository's synthetic archive:

```sh
REHEARSE_TEST_LINUX=disposable \
REHEARSE_TEST_BACKUP="$PWD/examples/miniflux/miniflux-source-125.dump" \
go test -count=1 -v ./internal/app -run '^TestCompleteMinifluxRehearsalOnExplicitDisposableLinuxRuntime$' -timeout 20m
```

On Windows, set `REHEARSE_TEST_WSL` to the explicit disposable WSL2 distribution and `REHEARSE_TEST_BACKUP` to the absolute example path; select `TestCompleteMinifluxRehearsalOnExplicitDisposableWSLRuntime` instead. Never point qualification at production data or an engine with production workloads. Coordinate runtime tests sharing an engine.

## Change expectations

For a defect, reproduce the failure with a focused test before fixing it. Preserve the failing observation and subsequent verification. Do not weaken checks, discard failures, or treat a successful container healthcheck as application acceptance.

Keep input and report formats versioned. Adapter additions need pinned images, explicit supported pairs, retained-data and expected-transformation checks, real API/file checks, clean old-backup recovery, bounded inputs, and ownership-safe cleanup. Document unsupported scenarios visibly.

Submit one cohesive change with its purpose, test evidence and remaining limitations. Contributions are licensed under Apache-2.0.
