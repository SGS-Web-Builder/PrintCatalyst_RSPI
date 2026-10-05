#!/bin/sh
# Explicit opt-in to desktop auto-login on Raspberry Pi OS with LightDM.
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run with sudo on the target Pi.' >&2; exit 1; }
[ "$#" = 1 ] || { echo 'Usage: sudo enable-kiosk-boot.sh DESKTOP_USER' >&2; exit 1; }
case "$1" in ''|*[!a-zA-Z0-9_-]*) echo 'Invalid desktop username' >&2; exit 1;; esac
account="$1"
uid=$(id -u "$account")
[ "$uid" -ge 1000 ] || { echo 'Use an ordinary desktop user, not root or a service account.' >&2; exit 1; }
command -v chromium >/dev/null 2>&1 || command -v chromium-browser >/dev/null 2>&1 || { echo 'Install Chromium first.' >&2; exit 1; }
command -v lightdm >/dev/null 2>&1 || { echo 'This helper requires Raspberry Pi OS Desktop with LightDM. Configure your display manager manually.' >&2; exit 1; }
base=/usr/local/lib/printcatalyst-kiosk
test -r "$base/screen-launcher.py"
runuser -u "$account" -- python3 "$base/screen-autostart.py"
install -d -o root -g root -m 0755 /etc/lightdm/lightdm.conf.d
config=/etc/lightdm/lightdm.conf.d/60-printcatalyst-kiosk.conf
[ ! -L "$config" ] || { echo 'Refusing symlink configuration.' >&2; exit 1; }
printf '[Seat:*]\nautologin-user=%s\nautologin-user-timeout=0\n' "$account" > "$config"
chmod 0644 "$config"
systemctl enable cups.service printcatalyst-kiosk.service lightdm.service
systemctl set-default graphical.target
echo 'Boot configuration installed. No reboot or printer job was started.'
echo 'Pair the touchscreen once after the next login. Pairing persists until replaced or credentials/profile are removed.'
