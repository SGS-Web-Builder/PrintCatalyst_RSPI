# Cross-chat handoff

Last updated: 2026-10-03 (Asia/Kolkata).

## Current status

Reusable application code has been imported in this independent repository, with module paths renamed and provenance in SOURCE-IMPORT.json. Pickup core, verified-payment issuance/recovery, secret-scoped portal display, production dispatch restriction, Linux licence-device protection, service scaffolding and paid-file retention protection are implemented. Linux ARM64 compilation passes. This is **not** a ready-to-install kiosk: actual Linux startup qualification, preparation/rendering/printing, automatic touchscreen startup and hardware qualification are unfinished. No hardware test has run.

Local folder: C:/Users/sarth/Documents/PrintCatalyst-OnPremise/PrintCatalyst_RSPI
Origin: https://github.com/SGS-Web-Builder/PrintCatalyst_RSPI.git
Windows reference: C:/Users/sarth/Documents/PrintCatalyst-OnPremise

The Windows reference has active/uncommitted work from other chats. Never assume its HEAD contains the current files. Do not overwrite it or include its Git history/builds/credentials in a blind recursive copy.

## Latest user intent

Keep Windows product independent. Build a separate Pi touchscreen kiosk with the same portal, dashboard, mobile read-only merchant login, Cloudflare/domain/QR, Razorpay and other shop functionality. Customers pay on their phone and see a four-digit pickup code in the portal. Entering it on the kiosk releases the order quickly. No SMS. Recent licence decision: one-time activation, no recurring checks.

## Next task

Continue Phase 2 qualification and Phase 3 integration. Read PICKUP-IMPLEMENTATION.md and LINUX-SECURITY.md. Startup pickup keys, verified capture/reconciliation recovery and portal display are now wired. The authenticated loopback listener and durable rate limits are now implemented. Touchscreen UI and manual secure pairing are implemented (see latest session log). Next implement immutable preparation/CUPS output and automatic desktop startup. Do not issue codes from manual paid-state updates. Keep Claim restricted to the dedicated authenticated listener and preserve its durable limits. Preparation/CUPS integration must validate the stored digest before submission. Preserve the dispatcher guard; do not reimport or modify Windows code.

## Latest milestone — verified payment to portal pickup

### Subsequent milestone: protected kiosk release API

Added internal/kiosk (HTTP boundary, persistent rate limiting, lifecycle and Linux credential loader), migration 037, and supervisor startup wiring. The dedicated listener binds only 127.0.0.1:8081. It requires exact local Host/Origin, an independent bearer credential, JSON and no forwarded headers; it exposes no merchant routes and is not mounted on the portal. Responses contain only states, no order/customer/code data. Every authenticated attempt is persisted before lookup; limits survive restart and clock rollback. Initial limits: 5 attempts/30 seconds per physical kiosk and 30/5 minutes globally, escalating cooldowns. These are prototype defaults requiring usability testing.

The Linux provisioning script/service unit now includes a separate kiosk-client.key credential. Existing development setups must rerun provisioning and update the unit without replacing the master key. No credentials were generated or services installed on this host. MarkPrepared now validates a SHA-256 hex digest rather than length alone. Actual artifact validation/CUPS preparation is still absent, so normal orders cannot yet progress beyond Preparing.

Passed this continuation:

- `go test ./internal/kiosk ./internal/store -count=1 -timeout=90s`.
- `go test ./internal/kiosk ./internal/pickup ./internal/devicekeys ./internal/printers/dispatch -run 'TestKiosk|TestMalformedRequests|TestPickup|TestEncrypted|TestDevice|TestRejectInvalid' -count=1 -timeout=90s`.
- Linux ARM64 `go build ./...` and `go vet ./internal/kiosk ./internal/devicekeys ./internal/pickup`.
- `sh -n packaging/linux/provision-device.sh` and `git diff --check`.

Tests cover origin/host/peer/credential/proxy rejection, absence of merchant routes, durable/escalating/global quotas, concurrent reservations, malformed payloads consuming quota, database failure blocking claims, unprepared orders retaining codes, inactive licences blocking claims and one successful claim only. HTTP handler tests ran locally; actual Pi listener/systemd startup, touchscreen credential delivery and physical print remain untested. See KIOSK-ENDPOINT.md. No Windows-product edits, commit, push or deployment.

Implemented migration 036's durable verified-capture work queue. Merchant webhook processing checks provider ownership; capture checks expected gateway link/payment identity and order/intent amount/currency. Capture and work-item insertion commit atomically. Duplicate callbacks can recover after an interrupted transaction. Generic paid status, manual approvals and legacy callbacks without a pre-existing expected gateway link cannot generate pickup work. Authenticated API reconciliation uses the same capture path.

Linux startup derives separate pickup encryption/lookup keys through devicekeys, starts a one-second recovery worker and wires the portal service. GET `/api/v1/portal/orders/{id}/pickup` requires the matching `X-Order-Token` header and returns no-store data. No public release endpoint was added. The customer portal renders four-digit codes and preparation/claim/expiry/assistance messages, clears old codes, and resumes from its existing saved order receipt. Kiosk cash checkout is disabled; saved merchant payment-button settings remain intact. No SMS or licence network work was added to release.

Changed files include payments/service.go and pickup_capture.go/tests, pickup/recovery.go and platform files, main startup, localserver portal_pickup.go/tests and portal/catalogue/server wiring, portal HTML/CSS/JS, portal JS tests, migration 036 and migration-count test. Recovery stores only a generic error condition and retries failed issuance. Codes remain encrypted at rest. Preparing remains the normal state until the future artifact-preparation worker is implemented.

Validation:

- `go test ./... -timeout=4m` passed (log: ignored go-pickup-tests.log; localserver 234 seconds). This run preceded the final generic recovery-assistance addition.
- Final focused command: `go test ./internal/payments ./internal/pickup ./internal/store ./internal/localserver -run 'TestVerifiedCapture|TestUnverifiedOrMismatched|TestCaptureAndPickup|TestCallbackCannot|TestPickup|TestEncrypted|TestLANReconciliation|TestPortalPickup' -count=1 -timeout=90s` passed. No store tests matched this filter; the full store suite passed in the full run.
- Final `node --test test/*.test.mjs`: 59 passed. Covers leading-zero display, code clearing and malformed-code rejection alongside retained portal regressions.
- Final Linux ARM64 `go build ./...` and `go vet ./internal/payments ./internal/pickup ./internal/localserver` passed. `git diff --check` passed.
- New Go cases cover duplicate capture, restart recovery, transaction rollback/retry, visible issuance failure/retry, invalid signature/provider/amount/currency/link/order price, manual-paid rejection, callback-supplied expected-link rejection and order-token isolation.

No real merchant payment, browser visual QA, Linux service boot or physical print was performed. No commit/push/deployment or Windows-product edits occurred. A usage-limit interruption during final approval review was resolved by the user's continuation; the final checks above subsequently executed successfully.

## Known technical blockers

The Pi repository still has no Linux print backend or PDF preview. Invoice rendering remains Windows-specific. Linux licence protection and LibreOffice discovery now exist, but service startup, credential permissions and conversion require execution on Linux/Pi. Linux USB/LAN driver compatibility and physical timings remain unknown. Do not claim that cross-compilation produces a working kiosk.

## For another account/computer

Clone this repository and read its documents; chat history and the Windows local folder are not automatically available. Obtain an authorized sanitized source snapshot of the Windows product for Phase 1. Do not request or upload live payment secrets, licence signing keys, customer files or databases. Use placeholders/test-mode fixtures.

## Copyable prompt

> Continue PrintCatalyst_RSPI. Read AGENTS.md, docs/HANDOFF.md, docs/PICKUP-IMPLEMENTATION.md and docs/LINUX-SECURITY.md. Confirm Git root/origin and work only inside the Pi repository. Preserve its uncommitted source import, pickup/dispatch guard, Linux devicekeys/service scaffold and retention protection. These payment/portal integrations are now implemented. The trusted listener and durable throttling are implemented. Next build trusted touchscreen credential/session delivery, immutable preparation/CUPS and keypad UI. Trusted kiosk authentication/throttling, immutable preparation/CUPS, keypad UI and Linux/Pi qualification remain pending. Never treat paid status alone as physical release. Record tests and limitations in HANDOFF.md; no hardware printing is qualified.

## Session log

- 2026-10-03: Cloned empty new repository into nested independent folder. Added requirements, architecture, technology/migration map, staged plan, test plan, decision log and agent instructions. No runtime changes or hardware tests.
- 2026-10-03 continuation in this chat: confirmed independent Git root/origin and preserved the existing untracked import. Edited only this Pi repository. Added migration 035, internal/pickup/service.go and tests, printers/dispatch/kiosk.go and tests; updated dispatcher defaults/filter/direct/manual/invoice paths and migration-count test. Updated README, implementation plan, handoff and pickup design boundary documentation. No commit, push, installation or deployment performed; no other agent was delegated work.

## Verification from this continuation

- Repository root: `node --test test/*.test.mjs` — 57 passed.
- runtime: `go test ./...` — imported baseline passed (full suite; output in ignored go-baseline.log).
- runtime: `go test ./internal/pickup ./internal/printers/dispatch ./internal/store` — passed after adding the core/guard/migration.
- runtime: `go test ./internal/pickup ./internal/printers/dispatch ./internal/store -run 'TestPickup|TestEncrypted|TestKiosk|TestMigrations' -count=1 -timeout=60s` — pickup/dispatch tests passed after the final invoice guard and restart/off-mode assertions. The store package had no tests matching this final filter; its full suite passed in the preceding command.
- runtime: `go vet ./internal/pickup ./internal/printers/dispatch` — passed.
- runtime with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`: `go build ./...` — passed; compilation only, not a Pi boot/print test.

New tests cover duplicate issuance, unpaid rejection, Preparing without consumption, concurrent claims, persistence across service reconstruction, expiry without payment loss, malformed codes, leading-zero encrypted recovery, ciphertext tampering, automatic/manual/direct dispatch guards, invoice isolation, auto-print-off release and no repeated submission after dispatcher reconstruction.

## Active limitations

A secret-scoped GET pickup endpoint and verified-capture recovery worker now exist. Claim is now exposed only on the separately authenticated 127.0.0.1:8081 listener, with durable quotas. IssueVerified remains privileged; production issuance uses the durable verified-capture queue, never a generic paid state. MarkPrepared requires a real preparation worker; stored digests are not yet verified against immutable print artifacts by the inherited dispatcher. Source-file retention now preserves paid uncollected files; prepared-artifact lifecycle remains pending. Linux protection requires a provisioned systemd credential and real Pi serial; no fallback exists. CUPS/rendering remain absent. The publisher protocol still uses the inherited product identifier and needs Pi issuance qualification. These are implementation tasks, not reasons to disable checks or claim a working kiosk.

## Linux prerequisite continuation — 2026-10-03

- Defined Linux protection in LINUX-SECURITY.md before implementing it. Added internal/devicekeys (AES-GCM envelopes, purpose-separated HMAC keys, serial binding, Linux credential-file permission/owner/type/size checks), Linux licence protection, Pi data path, and Linux LibreOffice executable discovery. Existing in-memory, one-time licence checks remain unchanged.
- Added packaging/linux service unit, explicit provisioning script and development setup instructions. No service/key/account was provisioned on this Windows computer. Added .github/workflows/pi-checks.yml for Linux tests and ARM64 compilation; not pushed or run remotely.
- Adapted autodelete age policy to preserve paid/dispatched and nonterminal kiosk orders. Added expired-pickup/failed-pickup/completion regression coverage and updated inherited paid-file expiry expectations to Pi requirements. Order details remain retained.
- Passed: `go test ./internal/devicekeys ./internal/config ./internal/configparser ./internal/licensegate`; `go test ./internal/autodelete -count=1`; `go test ./internal/pickup ./internal/devicekeys ./internal/config ./internal/configparser ./internal/licensegate ./internal/documents`; `go vet ./internal/devicekeys ./internal/licensegate ./internal/autodelete ./internal/documents`.
- Passed with GOOS=linux GOARCH=arm64 CGO_ENABLED=0: `go build ./...`; `go test -c -o ../build/devicekeys-linux-arm64.test ./internal/devicekeys`; changed-package `go vet`. The Linux-only credential permission tests were compiled, **not executed**. WSL is not installed; no Linux environment was installed during this task.
- Passed: `sh -n packaging/linux/provision-device.sh` using Git's shell; `git diff --check`. systemd unit execution/verification, actual Pi identity/permissions, real activation and LibreOffice conversion remain untested.
- Changes remain local and uncommitted. Windows product code/service untouched. Next: payment/pickup integration and trusted kiosk boundary, with Linux/hardware execution required before release.

## Touchscreen continuation — 2026-10-03

Implemented localhost touchscreen at http://127.0.0.1:8081/ with responsive CSS, touch/USB keypad, leading-zero preservation, idle input clearing, double-submit blocking, cooldown display, next-customer reset and explicit attendant guidance after ambiguous responses. Added session.go and embedded ui assets, session tests, test/kiosk-screen.test.mjs and root-only packaging/linux/pair-screen.py. Server supports short-lived one-use pairing tickets and 12-hour HttpOnly browser sessions, while retaining strict release request checks. Permanent bearer keys never enter browser assets, URLs or command arguments. Pairing/session state is memory-only and is revoked on service restart; new pairing replaces the prior screen. Pairing attempts use the durable existing quotas. Setup instructions are in packaging/linux/README.md.

Passed: `go test ./internal/kiosk -count=1 -timeout=90s`; `node --test test/kiosk-screen.test.mjs` (2 tests); Linux ARM64 CGO_ENABLED=0 `go build ./...`; Linux ARM64 `go vet ./internal/kiosk`. Tests cover ticket replacement/replay/expiry, session replacement/expiry, no unauthenticated ticket minting, asset proxy rejection, leading zeroes, double submit, input clearing and lost-response handling.

Final full web regression: 61 tests passed (`node --test test/*.test.mjs`). No actual browser visual QA, Pi service boot, pairing helper execution, touchscreen/USB device test or physical print was performed. Manual Chromium startup is documented; automatic desktop startup is not installed. Preparation/CUPS with immutable digest verification remains the next major implementation task. Ordinary orders still remain Preparing until that worker exists. No Windows changes, commit, push or deployment.

## Prepared bundle continuation — 2026-10-03

Added `runtime/internal/prepared` and `docs/PREPARED-OUTPUT.md`. This is the storage foundation for the preparation worker, not a finished rendering or CUPS path. The package atomically publishes bounded, canonical manifest/PDF bundles, binds order/line/queue/settings/invoice details into SHA-256 identity, verifies the entire bundle before returning any bytes, rejects corruption and supports idempotent/concurrent preparation. Linux directories must be service-owned/private; Linux file/directory sync and confined filesystem operations are implemented. No runtime wiring was changed and no orders are newly marked ready.

Passed: `go test ./internal/prepared -count=1 -timeout=90s`; final `go test ./internal/prepared ./internal/pickup -count=1 -timeout=90s`; Linux ARM64 CGO_ENABLED=0 `go build ./...`, `go vet ./internal/prepared`, and `go test -c -o ../build/prepared-linux-arm64.test ./internal/prepared`. The Linux-only ownership/permissions/symlink test was compiled, not run. Tests executed on Windows cover tampering, missing/truncated output, changed copies/queues, cross-order identity, restart/reopen, concurrent publication, input validation, no partial output and invoice paper/tray preservation.

Next: implement the trusted renderer/preparation worker and frozen-order transaction, resolve threshold invoice-group timing, then consume verified prepared bytes through CUPS and existing journals. Prepared retention/orphan cleanup and memory budgeting remain pending. Original uploads must never be reopened for printing after prepared bundle verification. No real Pi, renderer or printer test; no Windows-product edits, commit, push or deployment.
