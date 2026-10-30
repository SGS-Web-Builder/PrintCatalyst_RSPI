# PrintCatalyst kiosk setup — 31 October 2026

Target: Raspberry Pi 4B 8 GB, Raspberry Pi OS Desktop 64-bit, Epson EM-C800 on
Ethernet, and an HDMI/USB touchscreen. This guide includes the software from
the Pi repository's `codex/pi-kiosk-gap-fixes` branch. Use the **ARM64 licence-free**
archive. The amd64 archive is for Intel/AMD Linux, not this Pi.

The existing Pi OS and shop data can be kept. **Do not reimage the existing card
just to update this application.** First-day setup requires internet for packages,
Cloudflare and online payments. The physical Epson/touchscreen acceptance tests
below are still required; automated tests are not hardware qualification.

## 1. Bring these items

- Pi 4B, existing 64 GB microSD, proper Pi 4 USB-C power supply (5 V/3 A class),
  ventilated case/cooling; a cable alone is not a power supply.
- Touchscreen and its own specified power supply; micro-HDMI to the screen's
  HDMI input; USB touch-data cable from screen to Pi. HDMI carries the picture,
  not touch input. Check the screen manual if its connectors differ.
- Epson EM-C800, ink/paper installed, and its power cable.
- Router/switch with internet and two Ethernet cables: Pi to router, Epson to
  router. Do not directly connect Pi and Epson for this initial setup.
- Windows laptop on the same LAN, keyboard/mouse for initial setup, latest
  archive/checksum and this guide saved locally.
- Shop/domain/Cloudflare login and your own Razorpay credentials available privately.

Power the Pi off before connecting the display/card. Connect micro-HDMI to the
Pi port nearest USB-C power. Connect USB touch data and Ethernet, then power on.
Choose HDMI input on the screen. Set landscape orientation and the screen's
native resolution in Raspberry Pi OS display settings. Test touch targets at
all four corners. A rotated display may need separate touch mapping according
to the screen vendor; do not guess a calibration matrix.

For a completely new card only: Raspberry Pi Imager -> Pi 4 -> Raspberry Pi OS
(64-bit) with Desktop -> the correct microSD -> hostname `printcatalyst`, your
username/password, local timezone, SSH enabled. Writing erases the selected card.
The existing username in this setup is `admin`.

## 2. Confirm the Pi and its current IP

On the **Pi terminal**:

```bash
uname -m
cat /etc/os-release
hostname -I
date
```

Expect `aarch64` and Raspberry Pi OS/Debian. Record the new LAN IP: previous
hotspot addresses may no longer apply. The Windows PC and Pi must be reachable
on the same LAN for the transfer below. Use the Pi terminal to get the IP if
`printcatalyst.local` does not resolve.

If the desktop has no browser:

```bash
sudo apt-get update
sudo apt-get install -y chromium
```

Do not perform a distribution upgrade during commissioning. Use the existing
working 64-bit OS unless there is a specific compatibility problem.

## 3. Transfer the latest build from Windows

Run in **Windows PowerShell**, not inside SSH. Enter the current **Pi IP**:

```powershell
$piAddress = Read-Host "Enter the Raspberry Pi IP address"
$piBuild = 'C:\Users\sarth\Documents\PrintCatalyst-OnPremise\PrintCatalyst_RSPI\build'
scp "$piBuild\printcatalyst-pi-arm64-license-free.tar.gz" "admin@${piAddress}:~/"
scp "$piBuild\printcatalyst-pi-arm64-license-free.tar.gz.sha256" "admin@${piAddress}:~/"
ssh "admin@$piAddress"
```

Use the `admin` password you set on the Pi, not the Windows password. Password
typing in the terminal is intentionally invisible. The local Windows folder
above is specific to this development PC; change it if the package was copied
to another PC. Source code is on GitHub; the ignored `build/` archive is supplied
separately and is not downloaded merely by `git pull`.

## 4. Check pending work, back up, and install

On the **Pi**:

```bash
cd ~
sha256sum -c printcatalyst-pi-arm64-license-free.tar.gz.sha256
lpstat -W not-completed -o
```

On a completely fresh OS where `lpstat` is not installed yet, skip that command
until the installer supplies CUPS. Expect checksum **OK**. If it fails, stop and transfer the archive/checksum again.
If jobs are listed, or the dashboard shows unfinished/review output, inspect and
resolve it before updating. Do not automatically cancel or reprint old orders.

For an **existing installation**, once printing is idle:

```bash
sudo systemctl stop printcatalyst-kiosk
sudo sh -c 'umask 077; tar -czf "/root/printcatalyst-backup-$(date +%Y%m%d-%H%M%S).tar.gz" /etc/printcatalyst-kiosk /var/lib/printcatalyst-kiosk'
```

This backup includes credentials, database and customer files. Keep it private;
do not upload it to GitHub or paste it in a chat. Keep the device key and data
together. On a fresh Pi with no service/data, skip that backup block.

Then install in a new extraction directory:

```bash
release_dir="$(mktemp -d "$HOME/printcatalyst-update-XXXXXX")"
tar -xzf ~/printcatalyst-pi-arm64-license-free.tar.gz -C "$release_dir"
cd "$release_dir/printcatalyst-pi"
sudo sh install.sh
systemctl is-active printcatalyst-kiosk
curl --fail http://127.0.0.1:8080/healthz
```

Expect `active` and JSON with `"status":"ok"`. No licence activation is required.
The installer preserves existing state/keys, installs CUPS, IPP tools and renderer
dependencies, and starts the application. It does not select your Epson queue or
configure your domain automatically.

If install/start fails, stop here and collect:

```bash
sudo journalctl -u printcatalyst-kiosk -n 80 --no-pager
```

## 5. Configure the Epson printer, not the old Canon queue

On the Epson panel, complete ink/paper setup and find its **printer IP**. Reserve
that address in the router. Keep Pi and Epson on the same LAN. Enable IPP using
the Epson network/Web Config settings if disabled. The recommended path is CUPS
driverless IPP: **do not install the Canon CAPT driver or a Windows Epson driver**.

In a Pi terminal, enter the **Epson IP** when prompted (not the Pi IP):

```bash
read -r -p "Enter Epson EM-C800 IP address: " epson_ip
epson_uri="ipp://${epson_ip}/ipp/print"
python3 /usr/local/lib/printcatalyst-kiosk/configure-epson.py --uri "$epson_uri"
```

The helper must detect **EPSON EM-C800** and compatible document formats. This
probe sends no print job. `/ipp/print` is a common endpoint; if it fails, confirm
the printer's actual IPP endpoint before proceeding. Use its working `ipps://`
endpoint if TLS is configured. Never create a raw queue as a fallback.

With no active work, apply the verified profile:

```bash
sudo systemctl stop printcatalyst-kiosk
sudo python3 /usr/local/lib/printcatalyst-kiosk/configure-epson.py --uri "$epson_uri" --apply
sudo systemctl start printcatalyst-kiosk
lpstat -v Epson_EM_C800
lpstat -p Epson_EM_C800 -l
```

Verify the device URI contains `waitjob=false&waitprinter=false`. This removes
CUPS's remote-job-completion/printer-idle waits between separate documents. New
queue creation uses `-m everywhere`, which negotiates capabilities. Existing
queues can use `--queue EXISTING_NAME`; the helper preserves their driver and
will not overwrite a different device or change a queue with pending work.
An existing paused queue is not automatically resumed. A failed command is not
proof of successful configuration: inspect the output before a test order.

No documents are merged. Paper, duplex, colour, copies and trays remain per job.
The old Canon queue is not modified. Full detail: [Epson setup](EPSON-EM-C800.md).

## 6. Owner dashboard, business and prices

On the **Pi browser**, open **http://localhost:8080/**. Use your existing owner
login. Only on first setup, retrieve the setup token:

```bash
sudo cat /var/lib/printcatalyst-kiosk/owner-setup-token.txt
```

If owner setup is already complete, absence of this first-time file is not a
reason to reset the installation.

1. Save business name/logo, INR currency, timezone **Asia/Kolkata**, and customer
   details policy. Reopen to verify the saved values.
2. Discover/select **Epson_EM_C800**; confirm its capabilities, actual paper and
   tray. Send the dashboard's paper test, inspect it, and confirm the paper.
   Enable/select Epson for the relevant services. Leave Canon unselected.
3. Save prices for **B&W single-sided, B&W duplex, colour single-sided and colour
   duplex**, for each offered paper size. Reopen Prices to verify persistence.
   Configure optional printer-specific overrides only after shared rates exist.
4. Keep **OTP enabled** for your intended kiosk flow. Keep unpaid cash automatic
   printing disabled unless you deliberately want customers to print before
   payment. Do not select `all_documents` accidentally.
5. If using separator invoices, choose Epson's real invoice paper/tray. The queue
   threshold is based on waiting work; a single order may not produce an invoice.

Check the local customer portal at **http://localhost:8080/portal/**. A fresh test
upload must show the expected quote. If it says setup incomplete, inspect the
dashboard setup status and saved prices instead of resetting the database.

For Windows dashboard access (optional), in **Windows PowerShell**:

```powershell
ssh -N -L 18080:127.0.0.1:8080 "admin@$piAddress"
```

Keep that terminal open; browse **http://localhost:18080/** on Windows. Run this
on Windows, not inside a Pi SSH shell. The application binds loopback: a phone
cannot use `http://PI_IP:8080/portal/` with this default installation.

## 7. Connect and pair the touchscreen

Use the regular Pi desktop account (`admin` here), never root, for Chromium.
Confirm the USB touch cable works before testing OTP. A screen can display a
picture while its touch cable is missing.

On the **Pi desktop terminal** generate a short-lived pairing code:

```bash
sudo python3 /usr/local/lib/printcatalyst-kiosk/pair-screen.py
```

Open the pairing page in the **dedicated kiosk browser profile**:

```bash
chromium --user-data-dir="$HOME/.local/share/printcatalyst-screen" http://127.0.0.1:8081/admin/pair/
```

Enter the displayed code within two minutes. Generate a new code if it expires.
Do not share the pairing code or permanent credentials. Close this dedicated
Chromium window after successful pairing. Then launch the fullscreen screen:

```bash
python3 /usr/local/lib/printcatalyst-kiosk/screen-launcher.py
```

The launcher opens **http://127.0.0.1:8081/** using the same profile. Confirm:
full-screen ad background; floating numeric OTP box; no product header/footer;
all digits/backspace reachable by touch; no unpaired-screen warning. Fourth
digit submits automatically. Entering a code twice must never duplicate output.

Pairing survives restarts but belongs to this browser profile. Do not pair in a
different normal/guest/incognito browser and expect the kiosk profile to inherit
it. Only one active screen pairing is supported.

## 8. Start the kiosk automatically after boot

Do this after successful pairing and testing. The supplied helper supports
**LightDM** on Raspberry Pi OS Desktop. Check:

```bash
readlink -f /etc/systemd/system/display-manager.service
```

If it identifies LightDM, run (replace `admin` only if your desktop user differs):

```bash
sudo sh /usr/local/lib/printcatalyst-kiosk/enable-kiosk-boot.sh admin
```

This explicitly enables desktop auto-login, app/CUPS/display services and browser
autostart. It does not reboot. If it reports a different display manager, stop
and configure that manager's desktop auto-login instead; do not force LightDM.

After printing is idle, reboot once:

```bash
sudo reboot
```

Confirm desktop login, kiosk launch, pairing persistence, responsive touch and
network reconnection. The original Pi system user still has administrative
access; fullscreen browser mode alone is not a hardened public-access OS.

## 9. Cloudflare domain and phone QR

Use the existing Cloudflare account/domain. In the Cloudflare dashboard create
or select a named Tunnel, choose the Linux ARM64 connector instructions, and run
the supplied install/service command **on the Pi**. Keep its tunnel token private.
Do not paste a token into this guide or a support log.

Configure the public hostname, for example `print.yourshop.com`, to service
**HTTP -> http://127.0.0.1:8080**. Never publish kiosk port 8081 or CUPS port 631.
Cloudflare's connector must be on the same Pi as the application.

In PrintCatalyst's domain/QR settings choose Cloudflare, save the HTTPS origin
without `/portal/`, leave shop route empty, and set QR target `/portal/`. Click
**Verify origin**. Save/download the QR after verification. Do not put an
interactive login challenge on customer portal or Razorpay webhook routes.

```bash
systemctl is-active cloudflared
sudo journalctl -u cloudflared -n 40 --no-pager
```

Open the HTTPS portal on a phone using mobile data and scan the printed QR.
Saving a domain in the dashboard alone does not install/start a tunnel.
See [Cloudflare's current connector instructions](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/).

## 10. Payments and the full customer flow

Start with Razorpay **Test Mode**. Save the Test Mode Key ID/Secret in the local
dashboard. Register the exact webhook URL and event names shown by this build
in Razorpay, using the same separately configured webhook secret. Do not invent
a callback path or treat the API connection test as proof of webhook delivery.

Run on a phone: **QR -> upload -> choose settings -> verify total -> pay ->
return to portal -> see four-digit OTP -> enter on screen -> print**.

Check payment success is server-verified, no online unpaid order prints, and the
code survives payment return. The code expires at shop-local midnight. Browser
notifications are optional despite being selected by default: they require
HTTPS/browser support and permission, and must not be the only way to see OTP.
If preparation is still running, the screen may show processing before release.

After the whole flow passes, configure Live Mode keys and its webhook separately
and run a controlled real payment/order before customer use. Refund/recovery
actions must be based on actual order/output evidence, not automatic reprints.

## 11. Ads and remote owner access

On the verified HTTPS domain, use `/ads-admin/` with owner credentials to upload
images/videos, choose campaign durations and recurring time slots, and set the
default/fallback campaign. Validate an image and a modest 720p/1080p H.264 MP4 on
the real touchscreen. Video is muted; codec support and aspect cropping depend
on Chromium/device. Keep important text away from the OTP overlay.

Test uploading and printing while the video runs. Follow
[advertisement details](https://github.com/SGS-Web-Builder/PrintCatalyst_RSPI/blob/codex/pi-kiosk-gap-fixes/docs/TOUCHSCREEN_ADS.md).
Remote owner management is at `/remote/` on the same HTTPS domain.

Epson Remote Services (ERS) is separate from PrintCatalyst's print path. Its
integration here is not implemented/qualified; use Epson's supported onboarding
instructions/account if required. Do not install a guessed x86 Windows/Linux
agent on the ARM64 Pi. ERS is not needed to test this local CUPS IPP queue.

## 12. Acceptance before leaving the kiosk unattended

| Test | Required result |
|---|---|
| One B&W PDF; one colour PDF | Correct paper/colour/copies and complete output |
| Several separate documents in one order | Every document queued without waiting for previous physical completion |
| Merged images plus separate images | Merged page and all separate pages physically present |
| Duplex with odd page counts | Correct orientation and document boundaries |
| Copies/trays/invoice | Exact requested copies and correct trays; invoice when threshold applies |
| Payment return | Phone returns to portal and sees the correct OTP |
| Repeated OTP/reload/restart | No duplicate prints |
| Paused/offline/paper-out printer | Visible assistance, retained order, no blind replay |
| Video ad while printing | Touch remains usable; all documents print |
| Reboot | App/tunnel/screen recover and pairing remains valid |

Local CUPS acceptance, delivery to Epson and physical output are different
measurements. Earlier Canon software handoff was 139 ms for three files; this is
not an Epson guarantee. Epson's published first-page time is about4.8–5.3s.
Large transfers, rendering, paper handling and printer processing take longer
than milliseconds. CUPS completion does not automatically prove every physical
page; confirm output in the dashboard when required.

For timing/problem reports, run on the Pi:

```bash
sudo journalctl -u printcatalyst-kiosk --since '10 minutes ago' --no-pager -g 'print_preflight|print_submission'
sudo python3 /usr/local/lib/printcatalyst-kiosk/diagnose-print.py
lpstat -p Epson_EM_C800 -l
lpstat -W not-completed -o Epson_EM_C800
sudo journalctl -u cups --since '10 minutes ago' --no-pager
```

Do not delete the database, regenerate keys, or resubmit a partially printed
order to diagnose it. Note which physical pages arrived. `diagnose-print.py`
reports metadata, not document content, customer OTPs or credentials.

## Official references

- [Raspberry Pi hardware/OS setup](https://www.raspberrypi.com/documentation/computers/getting-started.html)
- [CUPS network printing and non-waiting IPP options](https://openprinting.github.io/cups/doc/network.html)
- [Epson EM-C800 specifications](https://download.epson.com.sg/product_brochures/EM-C800/WF_EMC800.pdf)

Prepared 30 October 2026. Actual printer, touchscreen, public tunnel and live
payment acceptance must be checked at the kiosk on 31 October 2026.
