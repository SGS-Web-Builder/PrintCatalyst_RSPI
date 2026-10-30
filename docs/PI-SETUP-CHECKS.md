> For the current Epson kiosk deployment, use [31 October setup](KIOSK-SETUP-2026-10-31.md). The Canon and historical network-approval notes below describe earlier testing.

# Pi shop setup and acceptance checks

1. On the Pi open http://localhost:8080/ and save the business profile and INR currency.
2. In Printers send one A4 test, inspect actual paper, confirm A4 and enable CanonLBP2900. Leave the old disabled URI-named duplicate unused.
3. In Prices choose All enabled printers — shared rates. Save A4 monochrome single-sided at 4.00 INR. Wait for Prices saved. Printer-specific overrides become available only after this base setup. Entering a number alone is not a successful save.
4. Reload http://localhost:8080/portal/. Upload a one-page file, select one A4 monochrome single-sided copy, and check the quote is INR 4.00. Existing confirmed order totals do not change.

## Network

The shipped service currently forces PC_BIND_HOST=127.0.0.1. This overrides saved direct-IP configuration and prevents LAN access. A change exposing port 8080 on all interfaces is pending explicit user approval after automatic approval review rejected that wider exposure. No listener change was applied in this audit.

Cloudflare can reach the existing loopback service: configure the tunnel public hostname to HTTP service http://127.0.0.1:8080 on the Pi. Never forward the separate port 8081 kiosk release listener. Install/run the Linux ARM64 cloudflared connector using the command from the merchant's Cloudflare account; keep its token private. Allow portal, health check and signed webhook traffic through the hostname without an interactive access challenge.

In the local dashboard choose Cloudflare, save the HTTPS origin without /portal/, leave shop route empty, set QR target /portal/, and Verify origin. Verification checks this installation's identity. A green tunnel alone does not prove prices, payment registration or physical printing.

## Razorpay and pickup acceptance

After HTTPS verification, configure Razorpay Test Mode Key ID and Key Secret. Generate and save a separate webhook secret. Copy the exact webhook URL and events shown by the app into Razorpay; use that same webhook secret. The API connection test is not proof of delivery.

Using a phone on mobile data, upload a one-page test file through the public portal, verify INR 4.00, complete a Razorpay Test Mode payment, and confirm the order becomes paid. Confirm it does not print before pickup. Enter its four-digit pickup code on the paired local kiosk, inspect the printed sheet, and verify the order finishes. Reusing the code must not create another print. Keep uncertain submissions for operator review rather than resubmitting automatically.

Only after those checks repeat setup with Live Mode keys/webhook. This audit uses automated local tests; live credentials, DNS, external webhook delivery and new hardware output were not tested by the agent.
