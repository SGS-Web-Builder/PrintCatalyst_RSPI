"""Install optional XDG kiosk autostart for the current unprivileged desktop user."""
import argparse
import os
from pathlib import Path
import shutil
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remove", action="store_true")
    args = parser.parse_args()
    if sys.platform != "linux" or os.geteuid() == 0:
        parser.error("Run as the Pi desktop user, without sudo")
    destination = Path.home() / ".config/autostart/printcatalyst-screen.desktop"
    if destination.is_symlink():
        parser.error("Refusing symlink autostart file")
    if args.remove:
        destination.unlink(missing_ok=True)
        return
    browser = shutil.which("chromium") or shutil.which("chromium-browser")
    if not browser or any(c in browser for c in '\n\r"`$\\%'):
        parser.error("Install Chromium using your OS package manager first")
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(
        '[Desktop Entry]\nType=Application\nName=PrintCatalyst Screen\n'
        'Exec=/usr/bin/python3 /usr/local/lib/printcatalyst-kiosk/screen-launcher.py\n'
        'Terminal=false\nX-GNOME-Autostart-enabled=true\n', encoding="utf-8")
    print("Autostart installed for this desktop user. Pair the screen once; its private browser profile preserves pairing across restarts.")
    print("Desktop login is still required; no automatic OS login was configured.")


if __name__ == "__main__":
    main()
