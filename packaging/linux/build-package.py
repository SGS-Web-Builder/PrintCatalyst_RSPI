"""Build an allowlisted ARM64 test/release archive without copying local data."""
import argparse
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[2]
FILES = {
    "screen-launcher.py": "packaging/linux/screen-launcher.py",
    "enable-kiosk-boot.sh": "packaging/linux/enable-kiosk-boot.sh",
    "install.sh": "packaging/linux/install.sh",
    "provision-device.sh": "packaging/linux/provision-device.sh",
    "printcatalyst-kiosk.service": "packaging/linux/printcatalyst-kiosk.service",
    "pair-screen.py": "packaging/linux/pair-screen.py",
    "screen-autostart.py": "packaging/linux/screen-autostart.py",
    "requirements.txt": "runtime/internal/printers/dispatch/renderer/requirements.txt",
    "README.md": "packaging/linux/README.md",
}


def build_args(development, public_key):
    args = ["go", "build", "-trimpath"]
    if not development:
        try:
            key = base64.b64decode(public_key, validate=True)
        except (ValueError, TypeError):
            raise ValueError("A base64 Ed25519 publisher public key is required")
        if len(key) != 32 or not any(key):
            raise ValueError("Publisher public key must be a nonzero 32-byte key")
        symbol = "github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/licensegate"
        args += ["-tags", "production", "-ldflags", f"-X {symbol}.ReleasePublicKey={public_key}"]
    return args


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--development", action="store_true")
    mode.add_argument("--public-key", help="Publisher PUBLIC verification key (base64)")
    opts = parser.parse_args()
    args = build_args(opts.development, opts.public_key)
    kind = "development" if opts.development else "production"
    output = ROOT / "build" / f"printcatalyst-pi-arm64-{kind}.tar.gz"
    output.parent.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="pi-package-") as temp:
        binary = Path(temp) / "printcatalyst-kiosk"
        env = dict(os.environ, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0")
        subprocess.run(args + ["-o", str(binary), "./cmd/print-catalyst-on-premise"],
                       cwd=ROOT / "runtime", env=env, check=True)
        payload = {name: (ROOT / source).read_bytes().replace(b"\r\n", b"\n")
                   for name, source in FILES.items()}
        payload["printcatalyst-kiosk"] = binary.read_bytes()
        manifest = {"mode": kind, "platform": "linux/arm64", "hardware_qualified": False,
                    "sha256": {name: hashlib.sha256(data).hexdigest() for name, data in payload.items()}}
        payload["manifest.json"] = json.dumps(manifest, indent=2).encode() + b"\n"
        payload["SHA256SUMS"] = "".join(f"{hashlib.sha256(data).hexdigest()}  {name}\n"
                                        for name, data in payload.items()).encode()
        temporary = Path(temp) / "package.tar.gz"
        with tarfile.open(temporary, "w:gz") as archive:
            for name, data in payload.items():
                info = tarfile.TarInfo("printcatalyst-pi/" + name)
                info.size = len(data)
                info.mode = 0o755 if name.endswith((".sh", ".py")) or name == "printcatalyst-kiosk" else 0o644
                archive.addfile(info, io.BytesIO(data))
        output.write_bytes(temporary.read_bytes())
    print(output)
    print("Not hardware qualified. Development builds are not merchant releases." if opts.development
          else "Production enforcement enabled; Pi activation and hardware acceptance still required.")


if __name__ == "__main__":
    main()
