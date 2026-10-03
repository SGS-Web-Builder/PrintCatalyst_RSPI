#!/bin/sh
# Explicit provisioning for a DEVELOPMENT Pi. Never replaces existing secrets.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Run as root on the target development Pi.' >&2; exit 1; }
[ -r /sys/firmware/devicetree/base/serial-number ] || { echo 'Pi board identity is unavailable.' >&2; exit 1; }
config=/etc/printcatalyst-kiosk
state=/var/lib/printcatalyst-kiosk
for path in "$config" "$state"; do
 [ ! -L "$path" ] || { echo 'Refusing a symlink installation directory.' >&2; exit 1; }
done
if ! id printcatalyst-kiosk >/dev/null 2>&1; then
 useradd --system --user-group --home-dir "$state" --shell /usr/sbin/nologin printcatalyst-kiosk
fi
[ "$(id -u printcatalyst-kiosk)" != 0 ] || { echo 'Service account must not be root.' >&2; exit 1; }
install -d -m 0700 -o root -g root "$config"
install -d -m 0700 -o printcatalyst-kiosk -g printcatalyst-kiosk "$state"
for name in device-sealing.key kiosk-client.key; do
key="$config/$name"
if [ -e "$key" ] || [ -L "$key" ]; then
 [ -f "$key" ] && [ ! -L "$key" ] && [ "$(stat -c '%s:%a:%u' "$key")" = '32:600:0' ] || {
  echo 'Existing credential is invalid; refusing to overwrite it. Recover from backup.' >&2; exit 1;
 }
else
 # noclobber prevents replacing a concurrently created credential.
 (set -C; head -c 32 /dev/urandom > "$key")
 [ "$(stat -c %s "$key")" = 32 ] || { echo 'Credential creation incomplete; explicit recovery required.' >&2; exit 1; }
fi
done
echo 'Device provisioned. Back up the credential securely with the database.'
echo 'No service was installed, enabled or started.'
