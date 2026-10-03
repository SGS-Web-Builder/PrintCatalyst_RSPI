# Requirements

## Approved by the owner

- Keep Windows and Raspberry Pi applications in separate repositories; no shared working source tree or automatic merging.
- Pi edition is a touchscreen kiosk with USB and/or LAN printers.
- Retain the customer portal, merchant dashboard, mobile merchant login/monitoring, Cloudflare/domain setup, QR configuration, Razorpay and other established shop features.
- Display a four-digit pickup code only in the customer portal after server-verified payment; no SMS.
- Correct code entry on the kiosk releases the matching order using customer-selected print settings.
- Prioritize release speed. Prepare immutable printable output before code entry when practical; a valid code must not release incomplete output.
- One-time licence activation, inherited from the latest Windows product decision. No recurring online checks.

## Feature parity inventory to verify during import

Uploads and previews; page selection including odd/even/skip; colour/B&W; copies; paper sizes; portrait/landscape/auto and correct duplex edge; images, merge and collage; merchant pricing and routing; printer test pages; payment return and reconciliation; order status/history; invoice separators and branding; retention/deletion; paper stock; staff permissions; QR/payment button policies; mobile read-only monitoring; licence setup and restart recovery.

This inventory is a migration checklist, not proof of Linux support.

## Proposed defaults — not yet owner-approved

- Numeric codes include leading zeros (0000–9999), generated with a cryptographic RNG and unique among outstanding orders on an installation.
- Default pickup validity: 24 hours. Expiry disables the old code; it does NOT cancel, refund or delete a paid order. Merchant can reissue a code after verification.
- Lock/rate-limit repeated failures at the kiosk, with progressive cooldown and an installation-wide limit. Final thresholds need usability testing.
- Kiosk online payment flow has no cash button by default. Existing merchant-configurable cash settings must be retained but their kiosk release semantics need confirmation before exposing cash checkout.
- Pi 4 Model B with at least 4 GB is the primary qualification target; Pi 3B is a constrained secondary target, not a performance promise.

## Needed before hardware acceptance

Exact Pi model/RAM; touchscreen model/resolution/rotation; printer model(s) and USB/LAN connection; driver/IPP support; desired code expiry; kiosk cash policy; response to printer outages and paper exhaustion. Development of the documented core can proceed without these answers.
