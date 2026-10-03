# Kiosk architecture

## Boundaries

Phone portal → public HTTPS through Cloudflare → local Pi service → SQLite/document store.
Touchscreen browser → dedicated local kiosk endpoint → the same service → durable release queue → CUPS/IPP → USB/LAN printer.
Merchant dashboard → owner-authenticated management routes. Public /monitor/ stays read-only with a separate session scope.

The Cloudflare route must never authorize kiosk release or owner writes. Use a separately bound loopback kiosk listener plus a provisioned kiosk credential/session. Reject forwarded headers there and do not proxy that listener through Cloudflare. Loopback alone is insufficient: cloudflared is itself local. Merchant endpoints also require owner authentication; prevent kiosk browser navigation from becoming administrative access.

## Independent state dimensions

Keep payment (pending/verified/failed/refunded), document preparation (pending/preparing/ready/failed), pickup (not-issued/active/claimed/expired), and print progress separate. Do not overload paid or dispatched to mean printed.

1. Upload and validate document limits, obtain settings and a server-priced quote.
2. Freeze the submitted order/settings/price and create payment intent idempotently.
3. Verify signed webhook or server reconciliation; confirm amount, currency and order identity.
4. Persist verified payment and allocate pickup code exactly once. Duplicate webhooks reuse it. Start/finish preparation; portal may show the code with a Preparing message.
5. Kiosk code lookup requires verified payment and ready output. If preparation is incomplete, show Preparing without consuming the code or submitting anything.
6. In a database transaction atomically claim the active code and create a durable release request. Commit before worker submission. Concurrent entries must produce one release request.
7. Worker journals document submissions and records external CUPS job IDs. Automatic scheduling may process only explicitly released kiosk orders, never every paid order.
8. Report progress from printer evidence. Submission is not proof of completed paper output. Unknown outcomes require operator review.

## Pickup code security and durability

Four digits are a pickup convenience, not merchant authentication. Use crypto/rand, preserve leading zeros, and enforce installation-wide active-code uniqueness in SQLite. Collision retries must be bounded. Exhaustion of the 10,000-code namespace is a visible recoverable condition, never code reuse.

Lookup codes with an installation-keyed HMAC rather than a bare hash (only 10,000 possibilities). The portal must redisplay an existing code after authenticated order recovery: retain an encrypted copy or an equivalent protected recovery mapping. Never put codes/order tokens in URLs or ordinary logs. Return them only to the order's existing secret-scoped portal session. Define encryption key storage, rotation and backup before implementation.

Rate-limit globally and per trusted kiosk/session; avoid order-detail disclosure on wrong guesses. Keep used/expired records for safe rejection and audit according to retention policy. Never automatically delete a paid uncollected order merely because its code expires.

## Printer failure and restart

Preparation is idempotent and tied to document/settings checksums. A crash before claim leaves the code usable; a crash after durable claim recovers the release request. A crash during external submission is ambiguous: reconcile stored job identity or request operator review. Do not advertise exactly-once physical printing without printer-level evidence. Partial multi-document failures retry only explicitly selected unresolved jobs after review.

Invoice jobs follow the same release and failure boundaries. Do not print an invoice immediately on payment, before pickup-code release. Keep source documents until retention rules allow deletion and prepared/submitted jobs no longer need them.

## Speed

Avoid gateway, licence-server, printer-discovery and document-conversion calls in the code-entry request. Cache validated capabilities and prepare output beforehand. The endpoint commits a release request and signals the worker; it must not block on physical printing. Measure payment verification → code availability, code entry → release claim, claim → CUPS acceptance, and CUPS acceptance → first physical output separately.
