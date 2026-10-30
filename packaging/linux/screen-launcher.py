"""Restart the dedicated kiosk browser and wait for the local service."""
import os
from pathlib import Path
import shutil
import subprocess
import time
import urllib.request


def main():
    browser = shutil.which("chromium") or shutil.which("chromium-browser")
    if not browser:
        raise SystemExit("Install Chromium first")
    profile = Path.home() / ".local/share/printcatalyst-screen"
    if profile.is_symlink():
        raise SystemExit("Refusing symlink browser profile")
    profile.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(profile, 0o700)
    import fcntl
    lock = (profile / "launcher.lock").open("w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    while True:
        try:
            with opener.open("http://127.0.0.1:8081/", timeout=3) as response:
                if response.status != 200:
                    raise OSError("Service not ready")
        except OSError:
            time.sleep(2)
            continue
        subprocess.run([browser, "--kiosk", "--no-first-run", "--no-default-browser-check",
                        "--disable-session-crashed-bubble", "--user-data-dir=" + str(profile),
                        "http://127.0.0.1:8081/"], check=False)
        time.sleep(3)


if __name__ == "__main__":
    main()
