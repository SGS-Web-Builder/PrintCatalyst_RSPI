# Development Pi startup

These files are scaffolding, not a qualified installer. Use a development Raspberry Pi with a 64-bit Linux OS and systemd supporting LoadCredential. Read ../../docs/LINUX-SECURITY.md first. No production licence signing keys belong on the Pi.

1. Compile the independent runtime for Linux ARM64. A merchant release must use `-tags production` and an approved publisher public key embedded in `internal/licensegate.ReleasePublicKey`; missing publisher configuration intentionally fails closed. The licence protocol currently retains the imported publisher product identifier; Pi-specific issuance and real activation must be qualified before release.
2. On the target Pi, run `sudo sh packaging/linux/provision-device.sh`. It creates a dedicated service account, private state directory and a random root-only credential. It never replaces an existing credential and does not start anything.
3. Install the ARM64 executable as `/usr/local/lib/printcatalyst-kiosk/printcatalyst-kiosk` (root-owned, mode 0755) and copy the unit to `/etc/systemd/system/printcatalyst-kiosk.service`. Keep the executable directory root-owned and not writable by the service account.
4. Run `sudo systemd-analyze verify /etc/systemd/system/printcatalyst-kiosk.service`, then `sudo systemctl daemon-reload` and `sudo systemctl start printcatalyst-kiosk`. Inspect `sudo journalctl -u printcatalyst-kiosk` for failures. Do not enable automatic customer use yet.
5. The merchant/setup listener defaults to `127.0.0.1:8080`. The separate credential-protected release listener now binds `127.0.0.1:8081`; never forward port 8081 through Cloudflare. See ../../docs/KIOSK-ENDPOINT.md for its request contract. Rerun provisioning and update the unit on existing development installs to add `kiosk-client.key`. Do not reuse the service account for Chromium; a trusted launcher/session bridge is still pending.
6. Optional DOCX conversion discovers `/usr/bin/libreoffice` or `/usr/bin/soffice`; install the distribution's LibreOffice package if required. Actual conversion under the service sandbox still needs Linux validation. CUPS, PDF/image preparation and physical printing remain unfinished.

Test first boot, unchanged-key restart, missing credential, bad permissions, copied database on a different Pi, correct offline licence restart, SIGTERM shutdown and service recovery. Keep encrypted backups of the root credential together with SQLite/customer artifacts. Never regenerate the credential to repair a startup error.

`LoadCredential` passes credential files through `CREDENTIALS_DIRECTORY`, with access restricted to the service user. See the official [systemd credentials documentation](https://systemd.io/CREDENTIALS/). This implementation uses a root-protected plaintext master key at rest; the encrypted licence private key is stored in SQLite. It does not claim hardware-backed master-key protection.

## Local touchscreen setup (development)

The runtime now serves the touchscreen and numeric keypad at http://127.0.0.1:8081/. Both touchscreen buttons and USB keyboard/numeric keypad input are supported. This is a separate listener from the public portal and has no merchant routes. Never tunnel port 8081.

1. Provision the Pi keys and start the runtime using the development service instructions above.
2. Open Chromium under a separate, unprivileged desktop account: `chromium --kiosk http://127.0.0.1:8081/` (binary naming depends on your Pi image).
3. From a local administrator terminal in this repository, run `sudo python3 packaging/linux/pair-screen.py`.
4. Open **Merchant · Pair this screen** on the touchscreen and enter the displayed 12-character code within two minutes. Do not save/share the code or capture it in logs.
5. Pairing grants this browser a 12-hour session. Pair again after expiry or a runtime restart. Pairing a new screen invalidates the previous session. Browser automatic startup is not installed by this change.
6. Customers enter their four-digit pickup code and press **Print my order**. An accepted response means queued, not physically printed. For an interrupted response, ask the attendant to check the order rather than resubmitting.

The helper reads only the separate root-owned kiosk credential. It disables environment proxies and HTTP redirects, sends the credential only to literal loopback, and displays only a temporary one-use pairing code. Chromium never receives the permanent credential. Do not grant the browser account access to the key or unrestricted sudo. This setup requires Linux/Pi acceptance testing; preparation/CUPS are still incomplete.
