"""Create deterministic parser-only data; not a restorable PostgreSQL fixture."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import random
import tarfile


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            result.update(chunk)
    return result.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--files", type=int, required=True)
    parser.add_argument("--bytes-per-file", type=int, required=True)
    args = parser.parse_args()
    if not 1 <= args.files <= 10000 or not 1 <= args.bytes_per_file <= 1024 * 1024:
        parser.error("dataset bounds exceeded")
    args.directory.mkdir(parents=True, exist_ok=False)
    dump = args.directory / "database.pgdump"
    dump.write_bytes(b"PGDMP" + b"parser-only synthetic benchmark; not restorable\n")
    data = args.directory / "forgejo-data.tar"
    rng = random.Random(20261009)
    with tarfile.open(data, "w", format=tarfile.USTAR_FORMAT) as archive:
        for name in ("gitea", "gitea/attachments"):
            info = tarfile.TarInfo(name)
            info.type = tarfile.DIRTYPE
            info.mode = 0o755
            info.uid = info.gid = 1000
            archive.addfile(info)
        for index in range(args.files):
            content = rng.randbytes(args.bytes_per_file)
            info = tarfile.TarInfo(f"gitea/attachments/{index:06d}.bin")
            info.mode = 0o644
            info.uid = info.gid = 1000
            info.size = len(content)
            archive.addfile(info, io.BytesIO(content))
    facts = {
        "schemaVersion": 1,
        "kind": "offline-archive-parser-only",
        "restorablePostgreSQLBackup": False,
        "seed": 20261009,
        "files": args.files,
        "regularFileBytes": args.files * args.bytes_per_file,
        "bytesPerFile": args.bytes_per_file,
        "members": args.files + 2,
        "filesSHA256": {p.name: {"bytes": p.stat().st_size, "sha256": digest(p)} for p in (dump, data)},
    }
    (args.directory / "dataset.json").write_text(json.dumps(facts, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(facts))


if __name__ == "__main__":
    main()
