"""Build one portable CLI archive from a clean, committed checkout (Python 3.10+)."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tarfile
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DISTRIBUTION_MANIFEST = (
    "README.md",
    "LICENSE",
    "SECURITY.md",
    "CONTRIBUTING.md",
    "THIRD_PARTY.md",
    "docs/architecture.md",
    "docs/configuration.md",
    "docs/fixture.md",
    "docs/miniflux-adapter.md",
    "docs/quickstart.tr.md",
    "docs/rehearsal-flow.md",
    "docs/roadmap.md",
    "docs/state-storage.md",
    "docs/support.md",
    "examples/miniflux/README.md",
    "examples/miniflux/miniflux-source-125.dump",
    "examples/miniflux/rehearse.json",
    "third_party/go.LICENSE",
)


class PackageError(RuntimeError):
    pass


def _is_link_or_reparse(info):
    reparse_flag = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    return stat.S_ISLNK(info.st_mode) or bool(getattr(info, "st_file_attributes", 0) & reparse_flag)


def command(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True).strip()


def ensure_release_dir(root):
    root = Path(root).resolve(strict=True)
    if not root.is_dir():
        raise PackageError("repository root is not a directory")
    output = root / "release"
    try:
        output.mkdir(mode=0o755)
    except FileExistsError:
        pass
    try:
        info = output.lstat()
    except OSError as error:
        raise PackageError("release directory is unavailable") from error
    if _is_link_or_reparse(info) or not stat.S_ISDIR(info.st_mode):
        raise PackageError("release path must be a real directory")
    resolved = output.resolve(strict=True)
    if resolved != root / "release" or resolved.parent != root:
        raise PackageError("release directory escaped repository root")
    return resolved


def check_output_slots(output, archive_name):
    if (not archive_name or "/" in archive_name or "\\" in archive_name
            or Path(archive_name).name != archive_name or archive_name in {".", ".."}):
        raise PackageError("archive name is invalid")
    for name in (archive_name, archive_name + ".sha256"):
        if os.path.lexists(Path(output) / name):
            raise PackageError("archive or checksum already exists; preserve it or remove that exact file explicitly")


def collect_distribution_files(root):
    root = Path(root).resolve(strict=True)
    files = {}
    for name in DISTRIBUTION_MANIFEST:
        parts = PurePosixPath(name).parts
        path = root
        for index, part in enumerate(parts):
            path = path / part
            expected = root.joinpath(*parts[:index + 1])
            try:
                info = path.lstat()
                resolved = path.resolve(strict=True)
            except OSError as error:
                raise PackageError("required distribution file is unavailable") from error
            expected_type = stat.S_ISREG if index == len(parts) - 1 else stat.S_ISDIR
            if (_is_link_or_reparse(info) or not expected_type(info.st_mode)
                    or os.path.normcase(str(resolved)) != os.path.normcase(str(expected))):
                raise PackageError("distribution inputs must be regular files beneath the source root")
        files[name] = (path.read_bytes(), 0o644)
    return files


def _require_regular_file(path):
    path = Path(path)
    try:
        info = path.lstat()
    except OSError as error:
        raise PackageError("staged package file is unavailable") from error
    if _is_link_or_reparse(info) or not stat.S_ISREG(info.st_mode):
        raise PackageError("staged package input must be a regular file")
    return path.resolve(strict=True)


def _remove_if_same_file(staged, published):
    try:
        if os.path.samefile(staged, published):
            Path(published).unlink()
    except FileNotFoundError:
        return


def publish_staged_pair(root, archive_name, staged_archive, staged_sidecar):
    output = ensure_release_dir(root)
    check_output_slots(output, archive_name)
    staged_archive = _require_regular_file(staged_archive)
    staged_sidecar = _require_regular_file(staged_sidecar)
    stage_dir = staged_archive.parent
    try:
        stage_info = stage_dir.lstat()
        resolved_stage_dir = stage_dir.resolve(strict=True)
    except OSError as error:
        raise PackageError("staged package directory is unavailable") from error
    if (staged_sidecar.parent != stage_dir or not stage_dir.name.startswith(".package-")
            or _is_link_or_reparse(stage_info) or not stat.S_ISDIR(stage_info.st_mode)
            or os.path.normcase(str(resolved_stage_dir)) != os.path.normcase(str(stage_dir))
            or resolved_stage_dir.parent != output):
        raise PackageError("staged package directory is outside the verified release directory")
    archive_path = output / archive_name
    sidecar_path = output / (archive_name + ".sha256")
    published_archive = False
    try:
        os.link(staged_archive, archive_path)
        published_archive = True
        os.link(staged_sidecar, sidecar_path)
    except BaseException as error:
        rollback_error = None
        if published_archive:
            try:
                _remove_if_same_file(staged_archive, archive_path)
            except OSError as cleanup_error:
                rollback_error = cleanup_error
        if rollback_error is not None:
            raise PackageError("partial package publication could not be fully rolled back") from rollback_error
        if isinstance(error, (KeyboardInterrupt, SystemExit)):
            raise
        raise PackageError("archive/checksum publication failed") from error
    return archive_path, sidecar_path


def write_archive(path, files, base, target_os):
    if target_os == "windows":
        with zipfile.ZipFile(path, "x", compression=zipfile.ZIP_DEFLATED) as archive:
            for name, (data, mode) in sorted(files.items()):
                info = zipfile.ZipInfo(f"{base}/{name}", date_time=(1980, 1, 1, 0, 0, 0))
                info.create_system = 3
                info.external_attr = (0o100000 | mode) << 16
                info.compress_type = zipfile.ZIP_DEFLATED
                archive.writestr(info, data)
        return
    with Path(path).open("xb") as raw:
        with gzip.GzipFile(fileobj=raw, mode="wb", filename="", mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as archive:
                for name, (data, mode) in sorted(files.items()):
                    info = tarfile.TarInfo(f"{base}/{name}")
                    info.size, info.mode, info.mtime = len(data), mode, 0
                    archive.addfile(info, io.BytesIO(data))


def sha256_file(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--os", required=True, choices=("windows", "linux"))
    args = parser.parse_args()
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", args.version):
        parser.error("version must be canonical x.y.z")
    if command("git", "status", "--porcelain"):
        parser.error("commit the reviewed source before packaging")
    commit = command("git", "rev-parse", "HEAD")
    try:
        output = ensure_release_dir(ROOT)
    except PackageError as error:
        parser.error(str(error))
    base = f"rehearse-{args.version}-{args.os}-amd64"
    archive_name = base + (".zip" if args.os == "windows" else ".tar.gz")
    try:
        check_output_slots(output, archive_name)
        files = collect_distribution_files(ROOT)
    except PackageError as error:
        parser.error(str(error))
    env = dict(os.environ, GOOS=args.os, GOARCH="amd64", CGO_ENABLED="0")
    with tempfile.TemporaryDirectory(prefix=".package-", dir=output) as temporary:
        stage_dir = Path(temporary)
        stage_info = stage_dir.lstat()
        resolved_stage_dir = stage_dir.resolve(strict=True)
        if (_is_link_or_reparse(stage_info) or not stat.S_ISDIR(stage_info.st_mode)
                or os.path.normcase(str(resolved_stage_dir)) != os.path.normcase(str(stage_dir))
                or resolved_stage_dir.parent != output):
            raise RuntimeError("temporary build directory escaped release root")
        binary_name = "rehearse.exe" if args.os == "windows" else "rehearse"
        binary = stage_dir / binary_name
        subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.version={args.version}", "-o", str(binary), "./cmd/rehearse"], cwd=ROOT, env=env, check=True)
        files[binary_name] = (binary.read_bytes(), 0o755)
        metadata = {"schemaVersion": 1, "product": "rehearse", "version": args.version,
                    "sourceCommit": commit, "sourceDirty": False, "go": command("go", "version"),
                    "target": f"{args.os}/amd64", "cgoEnabled": False,
                    "packageAcceptance": "not-run", "signed": False,
                    "files": {name: {"bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}
                              for name, (data, _) in sorted(files.items())}}
        files["BUILD.json"] = ((json.dumps(metadata, indent=2) + "\n").encode(), 0o644)
        if command("git", "status", "--porcelain") or command("git", "rev-parse", "HEAD") != commit:
            raise RuntimeError("source changed while building; archive was not written")
        archive_stage = stage_dir / archive_name
        sidecar_stage = stage_dir / (archive_name + ".sha256")
        write_archive(archive_stage, files, base, args.os)
        digest = sha256_file(archive_stage)
        with sidecar_stage.open("xb") as sidecar:
            sidecar.write(f"{digest}  {archive_name}\n".encode("ascii"))
        try:
            destination, _ = publish_staged_pair(ROOT, archive_name, archive_stage, sidecar_stage)
        except PackageError as error:
            parser.error(str(error))
    print(json.dumps({"archive": str(destination), "bytes": destination.stat().st_size, "sha256": digest, "sourceCommit": commit}))


if __name__ == "__main__":
    main()
