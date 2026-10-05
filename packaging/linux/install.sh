#!/bin/sh
# Explicit installation on a development Pi; does not start/enable customer service.
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run with sudo on the target Pi.' >&2; exit 1; }
[ "$(uname -m)" = aarch64 ] || { echo '64-bit ARM Linux is required.' >&2; exit 1; }
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
sha256sum -c SHA256SUMS
target=/usr/local/lib/printcatalyst-kiosk
[ ! -L "$target" ] || { echo 'Refusing symlink installation directory.' >&2; exit 1; }
if systemctl is-active --quiet printcatalyst-kiosk; then
 echo 'Stop the service after checking there are no active print jobs, then retry.' >&2
 exit 1
fi
sh ./provision-device.sh
apt-get update
apt-get install -y cups python3-venv fonts-noto-core
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
for helper in screen-launcher.py screen-autostart.py enable-kiosk-boot.sh; do
 install -o root -g root -m 0755 "$helper" "$target/$helper"
done
systemd-analyze verify /etc/systemd/system/printcatalyst-kiosk.service
systemctl daemon-reload
echo 'Installed. Configure CUPS and read README.md before starting the service.'
echo 'Service has not been started or enabled; credentials were preserved.'
