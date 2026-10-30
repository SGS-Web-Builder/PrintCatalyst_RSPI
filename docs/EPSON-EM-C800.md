# Epson EM-C800: consecutive document handoff on Raspberry Pi

## What this changes

Use the EM-C800 over wired Ethernet, with the Pi on the same LAN. The packaged
`configure-epson.py` checks the actual printer model and advertised formats, then
creates a CUPS driverless queue or updates an existing queue for that exact IPP
endpoint. It sets these **device URI** options, not per-document print options:

```
waitjob=false&waitprinter=false
```

CUPS can hand off subsequent documents without waiting for the previous remote
job to finish or the printer to become idle. Documents stay separate, with their
own copies, colour, sides, paper and tray. The application already submits them
consecutively without polling for completion. No raw PDF/port-9100 bypass,
document merging, quality reduction, or asynchronous unjournaled submission is
introduced. Ambiguous submission still requires review; the profile uses
`printer-error-policy=stop-printer` instead of blind queue retries.

This is not a Canon CAPT/USB driver patch. For this kiosk, prefer Ethernet to make
use of the documented IPP backend controls. It is not a claim that Ethernet is
always faster than USB. USB/IPP-over-USB needs its own device qualification.

## Install and configure on the Pi

The installer supplies `cups-ipp-utils` and installs the helper at
`/usr/local/lib/printcatalyst-kiosk/configure-epson.py`. Obtain the Epson's IP
address and IPP endpoint from its network settings/Web Config. Reserve its IP in
the router. The commonly used `/ipp/print` path must actually respond; do not
assume it if the probe fails. Enable IPP on the printer if necessary.

Replace the example IP address below with the **printer IP**, not the Pi IP.
Use `ipps://` instead if the printer provides a working trusted TLS endpoint.

First perform the read-only capability check (no test print is sent):

```bash
python3 /usr/local/lib/printcatalyst-kiosk/configure-epson.py \
  --uri ipp://192.168.1.50/ipp/print
```

Wait for active printing to finish. Check pending jobs before stopping the app:

```bash
lpstat -W not-completed -o
sudo systemctl stop printcatalyst-kiosk
sudo python3 /usr/local/lib/printcatalyst-kiosk/configure-epson.py \
  --uri ipp://192.168.1.50/ipp/print --apply
sudo systemctl start printcatalyst-kiosk
```

The helper refuses to change an existing queue with pending jobs, replace a
different device, or configure while the application is active. It does not
delete jobs, reset the printer, restart CUPS, or resume an existing paused queue.
For an existing Epson queue pass `--queue YOUR_QUEUE_NAME`; its driver and other
URI parameters are preserved. A new queue uses `-m everywhere`; capability
negotiation must succeed. Unsupported devices do not receive a raw fallback.
If configuration fails, the helper reports failure; inspect the queue before
use and restart the application. Do not assume a failed `lpadmin` was atomic.

In the shop dashboard, select `Epson_EM_C800`, refresh capabilities and confirm
paper, trays, duplex and pricing. The helper does not rewrite existing order
routes or frozen prepared jobs. Test **new orders**, not previously submitted
orders. The old Canon queue is not removed or modified.

## Timing and acceptance

The kiosk now schedules fourth-digit submission on the next event-loop turn,
without the earlier artificial 150 ms delay. Prepared PDFs, checksum validation,
payment authorization, and durable duplicate protection remain in place.

Measure separately:

1. OTP accepted to first/all documents accepted by local CUPS.
2. Local CUPS to acceptance by the Epson over the LAN, including any filter work.
3. Actual first-page and subsequent-page output.

The application logs only measure its own preflight/submission stages; they do
not prove remote acceptance. Earlier Canon traces showed three CUPS acceptances
within139 ms while paper output took much longer. That is not an Epson benchmark.
Milliseconds are an optimization target for small, already-prepared jobs, not
a universal maximum for all document sizes, queue conditions or network states.
For example50 MiB takes at least~4.2 seconds at100 Mbit/s before overhead.
Printer spooling, conversion and paper movement still take time.

After a new multi-document order, collect:

```bash
sudo journalctl -u printcatalyst-kiosk --since '10 minutes ago' --no-pager \
  -g 'print_preflight|print_submission'
sudo python3 /usr/local/lib/printcatalyst-kiosk/diagnose-print.py
lpstat -p Epson_EM_C800 -l
lpstat -W not-completed -o Epson_EM_C800
```

Qualify at least: ten small separate files; mixed colour/mono and copies; duplex
with odd-page boundaries; tray changes; invoice last; printer busy; offline or
paper-out; restart after submission. Check every physical page, settings and
duplicate count. Never report green physical completion solely from CUPS
completion: with this profile it is especially clearly a handoff event.

To restore waiting on an idle queue, stop the app, verify no pending work and use
the same device URI with `waitjob=true&waitprinter=true` through `lpadmin -v`.
Preserve its scheme, host, path and any other existing parameters. No automatic
rollback is performed while jobs may be active.

## Research and qualification status

- [OpenPrinting network backend options](https://openprinting.github.io/cups/doc/network.html): `waitjob=false` and `waitprinter=false` explicitly suppress completion/idle waits.
- [CUPS administration](https://openprinting.github.io/cups/doc/man-lpadmin.html): driverless `everywhere`, error policy and queue options.
- [Epson India EM-C800](https://www.epson.co.in/Business-Inkjet-Printers/WorkForce-Pro-EM-C800-Workgroup-Color-Multifunction-Printer/p/C11CK19503): up to25 ISO ppm.
- [Epson EM-C800 specifications](https://download.epson.com.sg/product_brochures/EM-C800/WF_EMC800.pdf): approximately4.8 seconds black/5.3 seconds colour first-page output; regional model/conditions matter.

The helper and regression tests are implemented. No live EM-C800, LAN transfer,
firmware or physical timing qualification has been performed by this chat.
