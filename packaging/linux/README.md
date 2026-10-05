# Development Pi startup

These files are scaffolding, not a qualified installer. Use a development Raspberry Pi with a 64-bit Linux OS and systemd supporting LoadCredential. Read ../../docs/LINUX-SECURITY.md first. No production licence signing keys belong on the Pi.

1. Compile the independent runtime for Linux ARM64. A merchant release must use `-tags production` and an approved publisher public key embedded in `internal/licensegate.ReleasePublicKey`; missing publisher configuration intentionally fails closed. The licence protocol currently retains the imported publisher product identifier; Pi-specific issuance and real activation must be qualified before release.
2. On the target Pi, run `sudo sh packaging/linux/provision-device.sh`. It creates a dedicated service account, private state directory and a random root-only credential. It never replaces an existing credential and does not start anything.
3. Install the ARM64 executable as `/usr/local/lib/printcatalyst-kiosk/printcatalyst-kiosk` (root-owned, mode 0755) and copy the unit to `/etc/systemd/system/printcatalyst-kiosk.service`. Keep the executable directory root-owned and not writable by the service account.
4. Run `sudo systemd-analyze verify /etc/systemd/system/printcatalyst-kiosk.service`, then `sudo systemctl daemon-reload` and `sudo systemctl start printcatalyst-kiosk`. Inspect `sudo journalctl -u printcatalyst-kiosk` for failures. Do not enable automatic customer use yet.
5. The merchant/setup listener defaults to `127.0.0.1:8080`. The separate credential-protected release listener now binds `127.0.0.1:8081`; never forward port 8081 through Cloudflare. See ../../docs/KIOSK-ENDPOINT.md for its request contract. Rerun provisioning and update the unit on existing development installs to add `kiosk-client.key`. Do not reuse the service account for Chromium; use the pairing helper described below.
6. Optional DOCX conversion discovers `/usr/bin/libreoffice` or `/usr/bin/soffice`; install the distribution's LibreOffice package if required. Actual conversion under the service sandbox still needs Linux validation. Install the renderer/CUPS prerequisites below before starting the service; physical printing remains unqualified.

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

The helper reads only the separate root-owned kiosk credential. It disables environment proxies and HTTP redirects, sends the credential only to literal loopback, and displays only a temporary one-use pairing code. Chromium never receives the permanent credential. Do not grant the browser account access to the key or unrestricted sudo. This setup requires Linux/Pi acceptance testing; real printer and systemd execution remain unqualified.


## Renderer and CUPS prerequisites (development Pi)

The runtime now wires the preparation worker, verified prepared PDFs, CUPS submission,
preview renderer and completed-bundle cleanup. This is compiled and unit-tested code,
not a physically qualified Pi release. Install these dependencies on a development Pi:

```sh
sudo apt-get update
sudo apt-get install cups python3-venv fonts-noto-core
sudo python3 -m venv /usr/local/lib/printcatalyst-kiosk/renderer-venv
sudo /usr/local/lib/printcatalyst-kiosk/renderer-venv/bin/python3 -m pip install -r runtime/internal/printers/dispatch/renderer/requirements.txt
sudo systemctl enable --now cups
```

Run the commands from this repository root. The pinned Python dependencies were tested
on Windows; wheel availability and execution on your ARM64 OS remain to be verified.
The service uses the fixed root-owned virtualenv path above. Do not give the service or
Chromium account write access to the virtualenv. The renderer embeds its Python program
inside the Go binary. Optional Word conversion still requires LibreOffice.

Check that these fonts exist on the Pi:
`/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf` and
`/usr/share/fonts/truetype/noto/NotoSansDevanagari-Regular.ttf`.
Configure the actual printer queue in local CUPS before enrolling it in the dashboard.
Do not expose CUPS or kiosk port 8081 through Cloudflare.

Acceptance sequence: upload and configure in portal; verify payment; confirm that the
order remains unprinted while preparing/awaiting pickup; enter its code at the paired
physical kiosk; confirm one document submission and applicable invoice; verify CUPS
completion and prepared-file cleanup while order history remains. Repeat with network
loss, service restart, wrong code, duplicate code, duplex, N-up, selected pages and a
multi-page Unicode invoice. Never treat a mocked CUPS response as proof of paper output.

## Build and install the self-contained test archive

From the repository root, run `python packaging/linux/build-package.py --development`.
This produces `build/printcatalyst-pi-arm64-development.tar.gz`. It contains only the
ARM64 executable, installation helpers, dependency list, this guide and checksums.
It excludes databases, uploads, credentials and source-tree private files. Checksums
protect against accidental corruption; they are not a publisher signature.

For a production-enforced build use `--public-key BASE64_PUBLIC_KEY` instead of
`--development`. This requires the actual publisher verification key; never supply
or ship a private signing key. Actual Pi licence issuance still needs qualification.
Development builds must not be distributed as merchant releases.

On a development Pi, extract the archive, enter `printcatalyst-pi`, and run
`sudo sh install.sh`. Internet access is needed for OS/Python packages. The installer
refuses an active application service and preserves existing device credentials.
It installs dependencies and verifies the unit, but does not start or enable the
application. Configure a CUPS queue, then follow the startup/pairing steps above.
In the extracted archive, helper paths are directly `provision-device.sh`,
`pair-screen.py`, `screen-autostart.py`, and `requirements.txt`.

Optional desktop autostart: run `python3 screen-autostart.py` as the desktop user,
without sudo. Chromium opens the kiosk after desktop login. Use `--remove` to undo.
This does not enable automatic OS login or bypass screen pairing. The desktop account
must remain separate from the service account and must not have credential access.

Before restarting for an update, confirm no printing is active and stop the service.
Never replay an ambiguous print job as part of installation/recovery.

## Expired pickup recovery

An owner can use **Replace expired pickup code** on a paid, unclaimed order in the
dashboard. The customer refreshes their receipt to see the replacement and must enter
it on the paired physical kiosk. Repeating the request preserves the replacement.
Claimed orders or orders with a submission journal cannot be reissued; review their
printer/output status instead. No recovery action records payment or automatically
releases a print. Operators and read-only mobile users cannot perform this action.


## Unattended touchscreen boot (optional)

After installation, configure the shop/printers and pair the screen in its dedicated Chromium profile. Pairing survives service/browser restarts; deleting the profile, replacing pairing or rotating the kiosk credential requires pairing again.

On Raspberry Pi OS Desktop **using LightDM**, run:

```sh
sudo sh /usr/local/lib/printcatalyst-kiosk/enable-kiosk-boot.sh YOUR_DESKTOP_USER
```

This explicitly enables desktop auto-login and the CUPS/application/display services. It does not reboot. The account must be an ordinary dedicated desktop user, separate from the service account. For a different display manager, configure auto-login through that OS's settings, then run `python3 screen-autostart.py` as the desktop user. The launcher waits for the service, uses a private persistent profile and restarts Chromium when it exits.

To undo auto-login, remove `/etc/lightdm/lightdm.conf.d/60-printcatalyst-kiosk.conf`; run `python3 screen-autostart.py --remove` as the desktop user to remove browser autostart. Reboot acceptance must check that the paired keypad appears without intervention.

## Failed preparation

Fix the underlying printer/configuration/document problem, then use the owner's **Retry document preparation** action. This retains the verified payment and requires customer pickup entry after preparation. Claimed orders and jobs with submission records cannot use this action. Ambiguous printer submissions require checking the physical output and printer queue before any explicit retry.
