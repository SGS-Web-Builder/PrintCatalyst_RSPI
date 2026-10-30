# Physical kiosk release boundary

Development component: local touchscreen UI and manual pairing are now implemented; see the session section below.

The Pi runtime starts a separate HTTP listener on **127.0.0.1:8081** through the supervisor. It exposes touchscreen assets, pairing/session endpoints and `POST /api/v1/kiosk/claim`, never merchant APIs. The portal remains on its existing listener. Never configure cloudflared, a reverse proxy or a LAN bind for port 8081.

## Credential and request contract

Provisioning now creates a second independent random 32-byte file, `/etc/printcatalyst-kiosk/kiosk-client.key`, root-owned and mode 0600. systemd passes it as `kiosk-client-key` through LoadCredential. Existing development installations must rerun the idempotent provisioning script and update the service unit; existing master/activation keys are not replaced.

A future trusted kiosk launcher must provide the client credential without exposing the master key. The API expects Authorization Bearer with the **unpadded base64url encoding** of the raw client key. Do not place it in URLs, web assets, command-line arguments, logs or the customer portal. Launcher/session provisioning and key rotation are still pending; no public bootstrap endpoint issues this credential.

Requests require loopback peer address, exact Host `127.0.0.1:8081`, exact Origin `http://127.0.0.1:8081`, one valid Authorization header, application/json and a body such as `{"code":"0007"}`. Forwarded, X-Forwarded-*, CF-* and X-Real-IP headers are rejected, including empty ones. Cross-site/same-site Sec-Fetch-Site values are rejected. Query strings, unknown JSON fields, trailing JSON and oversized bodies are rejected. No CORS allowance is emitted.

Responses contain only a state, never the code, order ID, customer name or document details:

| HTTP | State | Meaning |
|---|---|---|
| 202 | accepted | Durable release claimed; not proof of paper output. |
| 409 | preparing | Valid code but artifacts not ready; code remains active. |
| 400 | invalid_code | Invalid, used, expired or malformed input. |
| 403 | forbidden | Request boundary or authentication failed. |
| 429 | cooldown | Wait for the Retry-After seconds before another attempt. |
| 503 | assistance/unavailable | Licence, storage or service needs attention. |

Do not automatically retry claim requests in a tight loop. A lost successful response followed by retry returns invalid_code, while the persisted claim prevents a second release. A future keypad should show assistance rather than promise another print. No automatic resubmission of ambiguous printer jobs is added.

## Persistent limits

Migration 037 stores fixed physical-kiosk and installation buckets. Every authenticated attempt reserves capacity **before** decoding/lookup. Current prototype defaults are five attempts per 30 seconds per kiosk and 30 per five minutes globally. Exceeding limits applies escalating cooldowns, capped at a multiplier of 32, with escalation reset after ten idle minutes. These are development defaults awaiting real kiosk usability testing.

Both buckets are transactional and survive restart; clients cannot choose a new identity to reset them. Backwards wall-clock changes do not reset quotas. Unauthorized traffic does not consume the kiosk's legitimate quota. Database failure blocks claims. Multiple physical kiosks and individual pairing/revocation are not implemented.

## Readiness and remaining work

The handler calls only the cached in-memory licence check, rate-limit storage and the pickup claim transaction. It performs no gateway calls, conversions, printer discovery or external submissions. The Linux preparation worker renders and validates immutable artifacts before marking pickup ready. The existing dispatcher still requires a claim.

Next: trusted touchscreen credential/session delivery and keypad UI, immutable preparation/CUPS with digest verification, completion/status UI, merchant reissue, and real Pi qualification. The listener code was tested through HTTP handler fixtures; actual Pi/systemd listener startup has not been tested.

## Implemented touchscreen sessions — 2026-10-03

Local static UI now exists at `/`, `/kiosk.js`, `/kiosk.css`; these GETs allow an initial browser navigation without credentials or Origin, but retain exact Host, loopback, proxy-header and cross-site checks. The page contains no order data or credential. Its CSP allows only same-origin scripts/styles/connections and denies framing.

`POST /api/v1/kiosk/pairing-ticket` requires the permanent bearer credential and the existing strict request boundary. The root-only `packaging/linux/pair-screen.py` helper requests a random 48-bit (12 hex character) ticket, valid for two minutes. Only one ticket is active. `POST /api/v1/kiosk/session` consumes it once under a mutex and sets a random 256-bit HttpOnly, SameSite=Strict cookie scoped to `/api/v1/kiosk/`. Ticket attempts share the durable installation/kiosk limits. Cookies are deliberately HTTP-only loopback cookies without Secure; never put this endpoint behind a proxy. Session hashes persist in SQLite and are bound to the kiosk credential; service restarts preserve pairing. Ticket hashes remain memory-only. Pairing replaces the previous session and credential rotation revokes it. Cookies refresh on authenticated use; the stored session has a ten-year upper limit. The browser never receives the permanent bearer credential.

Claim now accepts that cookie or the existing bearer credential, with unchanged strict Origin/Host/proxy checks and durable claim limits. A local process able to read the browser profile/session or root key is outside this boundary. Kiosk sessions grant no merchant authorization. Automated desktop startup and physical Pi qualification remain pending; see packaging/linux/README.md for manual setup.


POST /api/v1/kiosk/progress uses the same protected boundary and a random receipt returned from successful claim. Receipts expire after 24 hours; only a state is returned, never customer details. Progress polling cannot release or retry an order.
