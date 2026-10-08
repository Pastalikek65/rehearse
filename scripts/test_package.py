import tempfile
from pathlib import Path
import unittest
import os
import stat
import tarfile
from types import SimpleNamespace
from unittest import mock
import zipfile

from scripts import package


class PackageOutputTests(unittest.TestCase):
    def test_distribution_contains_only_reviewed_manifest_files(self):
        expected = {
            "README.md", "LICENSE", "SECURITY.md", "CONTRIBUTING.md", "THIRD_PARTY.md",
            "docs/architecture.md", "docs/configuration.md", "docs/fixture.md",
            "docs/miniflux-adapter.md", "docs/quickstart.tr.md", "docs/rehearsal-flow.md",
            "docs/roadmap.md", "docs/state-storage.md", "docs/support.md",
            "examples/miniflux/README.md", "examples/miniflux/miniflux-source-125.dump",
            "examples/miniflux/rehearse.json", "third_party/go.LICENSE",
        }
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in expected:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(name.encode("utf-8"))
            unexpected = root / "examples/miniflux/.env"
            unexpected.write_text("must stay out of the package", encoding="utf-8")

            files = package.collect_distribution_files(root)

            self.assertEqual(set(files), expected)
            self.assertNotIn("examples/miniflux/.env", files)

    def test_release_directory_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "checkout"
            outside = Path(temporary) / "outside"
            root.mkdir()
            outside.mkdir()
            link = root / "release"
            try:
                os.symlink(outside, link, target_is_directory=True)
            except OSError as error:
                self.skipTest(f"directory symlinks unavailable: {error}")

            with self.assertRaises(package.PackageError):
                package.ensure_release_dir(root)
            self.assertEqual(list(outside.iterdir()), [])

    def test_release_reparse_point_is_rejected_without_symlink_privilege(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "checkout"
            root.mkdir()
            release = root / "release"
            release.mkdir()
            real_lstat = Path.lstat

            def lstat_with_reparse_point(path):
                if path == release:
                    return SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_file_attributes=0x400)
                return real_lstat(path)

            with mock.patch.object(Path, "lstat", autospec=True, side_effect=lstat_with_reparse_point):
                with self.assertRaises(package.PackageError):
                    package.ensure_release_dir(root)

    def test_existing_checksum_sidecar_is_preserved_and_blocks_slot(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release = root / "release"
            release.mkdir()
            stage = release / ".package-test"
            stage.mkdir()
            archive_name = "rehearse-0.1.0-linux-amd64.tar.gz"
            staged_archive = stage / archive_name
            staged_sidecar = stage / (archive_name + ".sha256")
            staged_archive.write_bytes(b"new archive")
            staged_sidecar.write_bytes(b"new checksum\n")
            sidecar = release / (archive_name + ".sha256")
            sidecar.write_text("preserve this checksum\n", encoding="ascii")

            with self.assertRaises(package.PackageError):
                package.publish_staged_pair(root, archive_name, staged_archive, staged_sidecar)

            self.assertFalse((release / archive_name).exists())
            self.assertEqual(sidecar.read_text(encoding="ascii"), "preserve this checksum\n")

    def test_partial_publish_removes_only_its_archive_link(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release = root / "release"
            release.mkdir()
            stage = release / ".package-test"
            stage.mkdir()
            archive_name = "rehearse-0.1.0-linux-amd64.tar.gz"
            staged_archive = stage / archive_name
            staged_sidecar = stage / (archive_name + ".sha256")
            staged_archive.write_bytes(b"new archive")
            staged_sidecar.write_bytes(b"new checksum\n")
            final_archive = release / archive_name
            final_sidecar = release / (archive_name + ".sha256")
            real_link = os.link

            def fail_sidecar_link(source, destination):
                destination = Path(destination)
                if destination == final_sidecar:
                    destination.write_bytes(b"foreign concurrent checksum\n")
                    raise FileExistsError("occupied by another writer")
                return real_link(source, destination)

            with mock.patch.object(package.os, "link", side_effect=fail_sidecar_link):
                with self.assertRaises(package.PackageError):
                    package.publish_staged_pair(root, archive_name, staged_archive, staged_sidecar)

            self.assertFalse(final_archive.exists())
            self.assertEqual(final_sidecar.read_bytes(), b"foreign concurrent checksum\n")
            self.assertEqual(staged_archive.read_bytes(), b"new archive")

    def test_successful_publish_keeps_both_files_after_stage_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release = root / "release"
            release.mkdir()
            stage = release / ".package-test"
            stage.mkdir()
            archive_name = "rehearse-0.1.0-linux-amd64.tar.gz"
            staged_archive = stage / archive_name
            staged_sidecar = stage / (archive_name + ".sha256")
            staged_archive.write_bytes(b"complete archive")
            staged_sidecar.write_bytes(b"sha256  archive\n")

            archive_path, sidecar_path = package.publish_staged_pair(
                root, archive_name, staged_archive, staged_sidecar
            )

            staged_archive.unlink()
            staged_sidecar.unlink()
            stage.rmdir()
            self.assertEqual(archive_path.read_bytes(), b"complete archive")
            self.assertEqual(sidecar_path.read_bytes(), b"sha256  archive\n")

    def test_archive_formats_have_stable_members_and_executable_modes(self):
        files = {"README.md": (b"docs", 0o644), "rehearse": (b"binary", 0o755)}
        base = "rehearse-0.1.0-linux-amd64"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            linux_a = root / "a.tar.gz"
            linux_b = root / "b.tar.gz"
            package.write_archive(linux_a, files, base, "linux")
            package.write_archive(linux_b, files, base, "linux")
            self.assertEqual(linux_a.read_bytes(), linux_b.read_bytes())
            with tarfile.open(linux_a, "r:gz") as archive:
                executable = archive.getmember(base + "/rehearse")
                self.assertEqual(executable.mode, 0o755)
                self.assertEqual(archive.getnames(), [base + "/README.md", base + "/rehearse"])

            windows_a = root / "a.zip"
            windows_b = root / "b.zip"
            package.write_archive(windows_a, files, base, "windows")
            package.write_archive(windows_b, files, base, "windows")
            self.assertEqual(windows_a.read_bytes(), windows_b.read_bytes())
            with zipfile.ZipFile(windows_a) as archive:
                executable = archive.getinfo(base + "/rehearse")
                self.assertEqual((executable.external_attr >> 16) & 0o777, 0o755)
                self.assertEqual(archive.namelist(), [base + "/README.md", base + "/rehearse"])


if __name__ == "__main__":
    unittest.main()
