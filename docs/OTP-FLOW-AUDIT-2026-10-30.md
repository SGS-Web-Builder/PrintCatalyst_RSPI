# Payment return and OTP release audit — 30 October 2026

The audited flow is QR portal → upload and server quote → hosted Razorpay payment → return to the same portal origin → verified paid status and private four-digit pickup code → document preparation → trusted local code claim → prepared PDF submission to CUPS.

## Changes

- Prevent checkout/retry navigation when the browser cannot persist the private receipt required after payment return. Retry a compact receipt without large file/receipt metadata first. Preserve the draft if both storage writes fail; explain the issue on the confirmation page. No gateway query parameter is treated as payment evidence.
- Explain before checkout that customers must return to the portal for their pickup code.
- Wake the dispatcher immediately after a successful durable kiosk claim. Notifications coalesce and never block the claim handler. Existing 20 ms polling remains for recovery; dispatch still checks preparation hashes, licence policy and submission journals. This is not a hardware latency guarantee.
- Restrict Windows-only activation tests to non-licensefree builds. They previously ran against intentionally disabled activation and panicked on a nonexistent device key. Tested both optional licensed and licence-free paths.

## Verification and limits

73 JavaScript tests passed. Targeted kiosk claim/notifier and dispatcher wake tests passed. ARM64 licence-free build succeeded and archive SHA256 entries verified. Broad Go suite results are recorded in HANDOFF.md and build/audit-2026-10-30.log; the initial run includes the subsequently corrected activation test configuration failure.

No Pi installation, real payment, Cloudflare account change or physical latency benchmark was performed. LAN listener remains unchanged. Browser receipt recovery is same-origin/same-browser; closing the tab or switching browsers can still lose session storage. No cross-device order access is promised.

On the Pi, use a Razorpay Test Mode order. Return to the portal, wait for preparation-ready guidance, enter the code once, verify CUPS submission and the physical sheet, then try the same code again to confirm it cannot reprint. Test lost network, delayed webhook, browser reload, failed printer and service restart separately before live rollout. An accepted CUPS job is not proof of physical completion.
