# Performance evidence

The real rehearsal measurements below use actual restorable, bundled synthetic backups. The later offline parser measurements use deliberately non-restorable PostgreSQL headers; they establish no database restore or upgrade throughput claim. No measurement here covers total Docker/WSL memory.

## Actual Windows package rehearsals

The same extracted Windows `0.2.0` archive from clean commit `9595ffd75a527416c7adbdd7a0bfdef9f6a927b0` was installed into separate fresh private profiles for each adapter. Its SHA-256 is `b2dedfa256fd3325722c4a000005c3794884d5ac8f21d0c2b94e8ca13763daaa`; the executable SHA-256 is `45e2da0cca470e7f2d5088712e75f3b181702dc1e1fe4d7bbfd17eaa310c298b`. These are beta measurements, not v1 performance guarantees.

| Adapter | Synthetic backup | Versions | Samples | Run duration, seconds | CLI peak working set, bytes | Checks passed |
| --- | ---: | --- | ---: | ---: | ---: | ---: |
| Miniflux | 51,897 bytes | 2.2.19 → 2.3.3 | 1 | 142.293621 | 18,276,352 | 23 / 23 |
| Forgejo | 78,904 bytes | 15.0.9 → 16.0.5 | 1 | 230.715312 | 19,496,960 | 25 / 25 |

Each run restored baseline, target and old-backup recovery instances, compared nonempty retained-data projections, produced privacy-checked JSON/HTML reports and cleaned its owned resources. Forgejo also compared selected Git/data files. The original fixture remained unchanged, repeated cleanup succeeded and final resource queries returned empty inventories. These small fixtures exercise the supported application workflow; they do not model a large installation.

The host and sampling caveats below also apply. The native Windows CLI used a local Docker socket through an explicitly selected disposable Ubuntu 24.04.5 WSL2 distribution, with Engine 29.8.2 and Compose 5.6.0. Images were already present from qualification; image downloads are excluded. Cache state was uncontrolled and other preparation shared the host. The timer spans CLI start through exit, including cleanup and approximately 10 ms memory polling. The peak measures only that CLI, excluding WSL, child processes and containers. One observation per adapter is not a percentile or sizing recommendation.

[Raw real-workload records](performance/windows-rehearsal-0.2.json) retain every sample, package identity, fixture digests, check states, data-projection digests and source evidence hashes, without credentials or local paths. To repeat, extract that exact release artifact in a fresh private profile and run its bundled example using the documented platform prerequisites. A different artifact, dataset or environment is a new measurement.

## Actual Linux ELF package rehearsals (WSL2)

The local Linux `0.2.0` archive from the same clean9595 source was separately extracted into fresh ext4 profiles in Ubuntu 24.04.5 inside WSL2, using its local Unix Docker socket. Its SHA-256 is `e3ba2acfe8c3a98315b4b86f0514a272b5fe1f12c831a7ff9ed3d6d05c1286b0`. This archive differs from the CI-built archive; its own acceptance is recorded here.

| Adapter | Samples | Run duration, seconds | Sampled CLI RSS peak, bytes | Checks passed |
| --- | ---: | ---: | ---: | ---: |
| Miniflux | 1 | 25.995 | 13,500,416 | 23 / 23 |
| Forgejo | 1 | 44.723 | 13,619,200 | 25 / 25 |

Both runs used the same bundled backups and version pairs described above. Their nonempty data projections matched across baseline, target and recovery; Forgejo file projections also matched. Source backups were unchanged, JSON/HTML reports remained readable after two cleanups, and final run-label inventories returned no containers, volumes or networks.

Linux memory is RSS sampled from `/proc` at about 50 ms intervals, so short peaks may be missed. The monotonic timer starts before launching the CLI and ends after exit. It includes startup, cleanup and polling; it excludes image downloads because the images were already present. Other qualification preparation shared the host and cache state was uncontrolled. Children, the Docker daemon, containers and WSL host work are excluded from RSS. These timer and memory methods differ from the Windows observations; the samples do not establish relative platform speed or equal total memory use. Linux acceptance here is native ELF execution inside WSL2, with separate Ubuntu CI evidence; it is not a measurement of every physical Linux installation.

[Raw Linux records](performance/linux-rehearsal-0.2.json) retain every sample, exact package/executable and fixture identity, check states, projections and source evidence hashes. Repeating the bundled example with the same archive, a fresh private Linux profile and prepared local Docker engine creates a new observation.

## Offline parser measurements

The measured binary was built from clean commit `42dd0f1a2761afa672e3e1129500abbd9b9c734f`, reported `rehearse 0.2.0-development`, and has SHA-256 `c4510094991ed72e8d2c598d65dffcc15bc50406bef48253f2380280797048db`. It is an exploratory source build, not a qualified release package.

The host was Windows x64, build 26300, with a Ryzen 9 8945HX and 32 logical processors. Other qualification work shared the host. Python's platform string identifies this build as Windows 11; that string is preserved verbatim in the raw records rather than used to infer the installed edition. Inputs had recently been generated and cache state was uncontrolled. Measurements include process launch and approximately 10 ms polling overhead. Peak working set is the Windows `GetProcessMemoryInfo` peak for the CLI process only; it excludes Docker, WSL and child/container memory.

| Regular data | Files | Operation | Repetitions | Wall time, seconds | CLI peak working set, bytes |
| --- | ---: | --- | ---: | --- | --- |
| 8 MiB | 128 | archive | 1 | 1.306349 | 11,145,216 |
| 8 MiB | 128 | plan | 3 | 0.044739 / 0.034859 / 0.034486 | 9,609,216 / 9,601,024 / 9,584,640 |
| 64 MiB | 512 | archive | 1 | 0.314942 | 13,586,432 |
| 64 MiB | 512 | plan | 3 | 0.086573 / 0.086211 / 0.086055 | 10,174,464 / 10,321,920 / 10,285,056 |

All recorded invocations exited zero. The single 8 MiB archive invocation took longer than the larger invocation; these samples do not establish a scaling curve, cold-cache result or benchmark guarantee. Both complete raw records retain dataset hashes, binary identity, every repetition and sampling counts: [8 MiB](performance/windows-parser-8MiB.json), [64 MiB](performance/windows-parser-64MiB.json).

## Reproduce the offline measurement

Use Python 3.10+ and Windows for the process memory driver. Build the referenced commit with Go 1.27 and preserve its binary identity; a different commit is a new measurement. The Windows driver checks the supplied revision against the binary's embedded clean VCS revision before creating its output directory, and rejects a binary changed during measurement. The generator uses deterministic seed `20261009` and USTAR, and refuses to reuse an existing destination. The measurement also requires a new output directory.

```powershell
python scripts/benchmarks/make_parser_dataset.py C:/Temp/rehearse-parser-8MiB --files 128 --bytes-per-file 65536
python scripts/benchmarks/measure_windows_parser.py --binary C:/Temp/rehearse.exe --dataset C:/Temp/rehearse-parser-8MiB --output C:/Temp/rehearse-parser-result-8MiB --source-commit 42dd0f1a2761afa672e3e1129500abbd9b9c734f
```

For the 64 MiB dataset use 512 files and 131072 bytes per file, and choose fresh dataset/output directories. The driver records one archive invocation and three full-envelope plan invocations. It does not invoke `run` or supply an API credential. A failed measurement exits with an error; this offline driver does not retain failed process stdout/stderr. Neither successful command validates the deliberately invalid PostgreSQL backup.
