import base64
import importlib.util
from pathlib import Path
import tarfile
import unittest
import hashlib
import json

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("builder", HERE / "build-package.py")
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class PackageTests(unittest.TestCase):
    def test_production_requires_valid_public_key(self):
        for key in (None, "", "invalid", base64.b64encode(bytes(32)).decode()):
            with self.subTest(key=key), self.assertRaises(ValueError):
                builder.build_args(False, key)
        key = base64.b64encode(bytes(range(32))).decode()
        args = builder.build_args(False, key)
        self.assertIn("production", args)
        self.assertIn(".ReleasePublicKey=" + key, args[-1])
        self.assertNotIn("production", builder.build_args(True, None))

    def test_archive_is_allowlisted_and_matches_checksums(self):
        path = builder.ROOT / "build/printcatalyst-pi-arm64-development.tar.gz"
        self.assertTrue(path.exists(), "Build development package first")
        with tarfile.open(path) as archive:
            members = archive.getmembers()
            expected = set(builder.FILES) | {"printcatalyst-kiosk", "manifest.json", "SHA256SUMS"}
            self.assertEqual({m.name for m in members}, {"printcatalyst-pi/" + n for n in expected})
            self.assertTrue(all(m.isfile() for m in members))
            data = {m.name.split("/", 1)[1]: archive.extractfile(m).read() for m in members}
        for line in data["SHA256SUMS"].decode().splitlines():
            digest, name = line.split("  ", 1)
            self.assertEqual(hashlib.sha256(data[name]).hexdigest(), digest)
        self.assertEqual(json.loads(data["manifest.json"])["mode"], "development")
        binary = data["printcatalyst-kiosk"]
        self.assertEqual(binary[:4], b"\x7fELF")
        self.assertEqual(binary[4], 2)  # ELF64
        self.assertEqual(int.from_bytes(binary[18:20], "little"), 183)  # AArch64
        self.assertNotIn(b"\r\n", data["install.sh"])


if __name__ == "__main__":
    unittest.main()
