# Performance evidence

These development measurements cover offline archive construction and planning only. The data includes a deliberately non-restorable PostgreSQL header; it must never be used as an application backup. No database restore, upgrade throughput or Docker memory claim follows from these results. Real rehearsal measurements remain pending.

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
