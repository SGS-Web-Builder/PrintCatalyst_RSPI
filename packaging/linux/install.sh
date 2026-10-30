#!/bin/sh
# Install and start the local Linux application.
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run with sudo on the target Pi.' >&2; exit 1; }
case "$(uname -m)" in aarch64|x86_64) ;; *) echo "64-bit ARM or x86 Linux is required." >&2; exit 1;; esac
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
sha256sum -c SHA256SUMS
python3 -c 'import json,platform; m=json.load(open("manifest.json")); expected={"aarch64":"arm64","x86_64":"amd64"}.get(platform.machine()); assert m["platform"]=="linux/"+str(expected), "Package architecture does not match this computer"'
target=/usr/local/lib/printcatalyst-kiosk
[ ! -L "$target" ] || { echo 'Refusing symlink installation directory.' >&2; exit 1; }
if systemctl is-active --quiet printcatalyst-kiosk; then
 echo 'Stop the service after checking there are no active print jobs, then retry.' >&2
 exit 1
fi
sh ./provision-device.sh
apt-get update
apt-get install -y cups cups-ipp-utils python3-venv fonts-noto-core
install -d -o root -g root -m 0755 "$target"
[ ! -L "$target/renderer-venv" ] || { echo 'Refusing symlink virtualenv.' >&2; exit 1; }
python3 -m venv "$target/renderer-venv"
"$target/renderer-venv/bin/python3" -m pip install -r requirements.txt
"$target/renderer-venv/bin/python3" -c 'import pypdfium2, PIL, reportlab, uharfbuzz'
for font in NotoSans-Regular.ttf NotoSansDevanagari-Regular.ttf; do
 test -r "/usr/share/fonts/truetype/noto/$font" || { echo "Missing font: $font" >&2; exit 1; }
done
install -o root -g root -m 0755 printcatalyst-kiosk "$target/printcatalyst-kiosk"
install -o root -g root -m 0644 printcatalyst-kiosk.service /etc/systemd/system/printcatalyst-kiosk.service
for helper in screen-launcher.py screen-autostart.py enable-kiosk-boot.sh pair-screen.py configure-epson.py diagnose-print.py; do
 install -o root -g root -m 0755 "$helper" "$target/$helper"
done
systemd-analyze verify /etc/systemd/system/printcatalyst-kiosk.service
systemctl daemon-reload
systemctl enable --now cups.service
systemctl enable --now printcatalyst-kiosk.service
python3 - <<'PY'
import time, urllib.request
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for attempt in range(60):
    try:
        with opener.open('http://127.0.0.1:8080/healthz', timeout=2) as response:
            if response.status == 200:
                break
    except OSError:
        pass
    time.sleep(1)
else:
    raise SystemExit('Service did not become ready. Run: sudo journalctl -u printcatalyst-kiosk -n 80')
PY
echo 'Dashboard: http://localhost:8080/'
echo 'Customer portal: http://localhost:8080/portal/'
echo 'First-time setup token: sudo cat /var/lib/printcatalyst-kiosk/owner-setup-token.txt'
echo 'Complete owner and printer setup in the dashboard before uploading customer orders.'
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] && command -v xdg-open >/dev/null 2>&1; then
 runuser -u "$SUDO_USER" -- xdg-open http://localhost:8080/ >/dev/null 2>&1 || true
fi
