# Customer payment notifications and daily pickup codes

Implemented 2026-10-30 in the Raspberry Pi product only.

## Customer flow

The payment notification checkbox is selected by default. The customer can turn it off. Browser permission is requested from the Pay button gesture; denied or unavailable permission does not prevent payment. Subscription setup has a bounded wait before payment navigation. The existing receipt-storage requirement still applies to payment return; push is not a replacement for reliable website recovery.

Once the server verifies capture and issues the pickup code, a background worker sends an encrypted browser push: Payment received — Tap to view your pickup code. The code itself is not displayed on the lock screen. Tapping opens the portal with that order's secret in a URL fragment; the portal removes the fragment and retrieves the authorized receipt. The notification contains a sensitive order capability, so users should protect access to their phone.

The code is issued only after verified payment and expires at midnight in the shop's configured timezone (Asia/Kolkata fallback). Kiosk claims, portal display and owner reissue use the same day boundary, including older rolling-24-hour codes. Owner-authorized reissue remains available for a paid, unprinted expired order. Used codes are reserved until expiry to avoid same-day reuse for another order. Four digits support at most 10,000 reserved codes at once.

At the touchscreen, the Pi checks the stored paid order, current-day expiry, preparation and unused code locally, consumes the claim atomically and wakes dispatch. No Razorpay or browser-push network call is introduced into claim/release. Actual timing depends on preparation, queue availability, CUPS and the printer; physical millisecond timing has not been measured.

## Deployment and acceptance

Install the rebuilt ARM64 licence-free package normally; migration 042 creates persistent VAPID keys and per-order subscriptions in the protected application database. No external push account or app installation is required. This implementation permits Google FCM and Mozilla push endpoints only, rejects private network destinations and redirects, and uses order-token authorization for subscription changes.

Use the configured HTTPS customer domain on Android, not an HTTP LAN IP. Test with Chrome/Firefox; unsupported providers fall back to website status. Background delivery depends on browser permission, OS battery/background limits, internet connectivity and the browser push provider. This build sends payment notifications only, not a second preparation-complete alert.

1. From a phone, scan the QR, upload a test PDF, leave notifications selected and grant permission.
2. Pay through Razorpay Test Mode. Confirm the return page shows the same order and pickup code after verified capture.
3. Repeat with the portal in the background. Tap the payment notification; confirm it opens the correct order, including when the original tab was closed.
4. Repeat with permission denied and checkbox off; payment and website receipt must still work.
5. Enter the code on the paired touchscreen once files are ready. Confirm one physical print; reused/expired/unpaid codes must not print.
6. Verify the shop timezone and near-midnight expiry. Do not change the Pi's clock during live orders.

Push retries transient failures up to eight attempts with a one-minute delay; expired/invalid endpoints are removed and subscription rows are cleaned after two days. Push failure does not change paid state or print eligibility. Live provider delivery and physical Pi printing remain device acceptance steps.
