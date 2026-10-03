# Decision log

| ID | Status | Decision |
|---|---|---|
| D01 | Approved | Separate Windows and Pi repositories; no shared mutable source. |
| D02 | Approved | Keep portal, dashboard, mobile monitoring, Cloudflare and Razorpay parity. |
| D03 | Approved | Four-digit pickup code displayed in portal only after verified payment; no SMS. |
| D04 | Approved | Physical touchscreen code entry releases print; payment alone does not. |
| D05 | Approved | One-time activation, no recurring online licence checks. |
| D06 | Proposed | ARM64 Raspberry Pi OS plus CUPS/IPP and Chromium kiosk. |
| D07 | Proposed | Prepare immutable printable files before pickup; show Preparing if not ready. |
| D08 | Proposed | 24-hour code expiry with paid-order preservation and merchant reissue. |
| D09 | Open | Cash checkout behaviour for an otherwise payment-gated kiosk. |
| D10 | Open | Exact Pi/touchscreen/printer models and qualified performance budget. |
| D11 | Partially implemented | Linux key storage uses root-provisioned systemd credentials plus Pi serial binding; see LINUX-SECURITY.md for limits. ARM renderer selection and hardware validation remain open. |

Supersede decisions explicitly; do not silently change payment, printing or security semantics across chats.
