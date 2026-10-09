"""Measure one offline CLI process, including its OS peak working set."""
import argparse
import ctypes
from ctypes import wintypes
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import tempfile
import time


class MemoryCounters(ctypes.Structure):
    _fields_ = [("cb", wintypes.DWORD), ("pageFaults", wintypes.DWORD)] + [
        (name, ctypes.c_size_t) for name in (
            "peakWorkingSet", "workingSet", "quotaPeakPaged", "quotaPaged",
            "quotaPeakNonPaged", "quotaNonPaged", "pagefile", "peakPagefile")]


def run_measured(command):
    counters = MemoryCounters()
    counters.cb = ctypes.sizeof(counters)
    read_memory = ctypes.WinDLL("psapi").GetProcessMemoryInfo
    read_memory.argtypes = [wintypes.HANDLE, ctypes.POINTER(MemoryCounters), wintypes.DWORD]
    read_memory.restype = wintypes.BOOL
    start = time.perf_counter()
    with tempfile.TemporaryFile() as stdout_file, tempfile.TemporaryFile() as stderr_file:
        process = subprocess.Popen(command, stdout=stdout_file, stderr=stderr_file,
                                   creationflags=subprocess.CREATE_NO_WINDOW)
        peak = 0
        samples = 0
        while True:
            if read_memory(wintypes.HANDLE(int(process._handle)), ctypes.byref(counters), counters.cb):
                peak = max(peak, counters.peakWorkingSet)
                samples += 1
            if process.poll() is not None:
                break
            time.sleep(0.01)
        stdout_file.seek(0)
        stderr_file.seek(0)
        stdout, stderr = stdout_file.read(), stderr_file.read()
    elapsed = time.perf_counter() - start
    if process.returncode != 0 or stderr or not samples or not peak:
        raise RuntimeError("measurement failed; this offline driver does not retain failed process output")
    return {
        "seconds": round(elapsed, 6), "peakWorkingSetBytes": peak,
        "peakMetric": "Windows GetProcessMemoryInfo PeakWorkingSetSize; CLI process only",
        "memoryQueries": samples, "exitCode": process.returncode,
        "stdoutSha256": hashlib.sha256(stdout).hexdigest(),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--dataset", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args()
    if os.name != "nt":
        parser.error("Windows-only process metric")
    if not re.fullmatch(r"[a-f0-9]{40}", args.source_commit):
        parser.error("source commit must be a full Git revision")
    metadata = subprocess.check_output(["go", "version", "-m", str(args.binary)], text=True)
    build = {}
    for line in metadata.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[0] == "build" and "=" in fields[1]:
            key, value = fields[1].split("=", 1)
            build[key] = value
    if (build.get("vcs.revision") != args.source_commit or build.get("vcs.modified") != "false"
            or build.get("GOOS") != "windows" or build.get("GOARCH") != "amd64"):
        parser.error("binary must embed the supplied clean Windows amd64 Git revision")
    initial_binary_hash = hashlib.sha256(args.binary.read_bytes()).hexdigest()
    args.output.mkdir(parents=True, exist_ok=False)
    facts = json.loads((args.dataset / "dataset.json").read_text(encoding="utf-8"))
    archive = args.output / "benchmark.zip"
    config = args.output / "rehearse.json"
    config.write_text(json.dumps({
        "schemaVersion": 1, "adapter": "forgejo", "sourceVersion": "15.0.9",
        "targetVersion": "16.0.5", "postgresVersion": "17.11",
        "backupPath": "benchmark.zip", "authEnvRefs": {"apiToken": "PARSER_ONLY_UNUSED_TOKEN"}
    }) + "\n", encoding="utf-8")
    results = []
    result = run_measured([str(args.binary), "archive", "--json", "--database", str(args.dataset / "database.pgdump"),
                           "--data", str(args.dataset / "forgejo-data.tar"), "--output", str(archive)])
    results.append(dict(operation="archive", repetition=1, **result))
    for repetition in range(1, 4):
        results.append(dict(operation="plan", repetition=repetition,
                            **run_measured([str(args.binary), "plan", "--json", str(config)])))
    if hashlib.sha256(args.binary.read_bytes()).hexdigest() != initial_binary_hash:
        raise RuntimeError("binary changed during measurement")
    result = {
        "schemaVersion": 1, "kind": "offline-parser-scaling",
        "sourceCommit": args.source_commit,
        "version": subprocess.check_output([str(args.binary), "--version"], text=True).strip(),
        "binarySha256": initial_binary_hash, "embeddedBuild": build,
        "platform": platform.platform(), "logicalProcessors": os.cpu_count(),
        "cacheState": "uncontrolled; recently generated inputs; no cold-cache claim",
        "scope": "archive packaging and full offline envelope planning; no valid PostgreSQL restore, Docker or upgrade performance claim",
        "dataset": facts, "results": results,
    }
    (args.output / "measurements.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result))


if __name__ == "__main__":
    main()
