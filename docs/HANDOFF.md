# Cross-chat handoff

Last updated: 2026-10-05 (Asia/Kolkata).

## Current status

The independent Pi runtime now includes verified-payment pickup codes, protected touchscreen release, Linux rendering, immutable prepared PDFs and CUPS dispatch wired at startup. This continuation adds persistent screen pairing, private print-progress receipts, one-PDF-at-a-time preparation/dispatch, owner-controlled preparation retry and opt-in desktop boot helpers. The ARM64 development package builds. Actual Pi startup, memory use, USB/LAN printers, payment-to-paper latency and touchscreen qualification remain mandatory; this is not a hardware-qualified release.

Local folder: C:/Users/sarth/Documents/PrintCatalyst-OnPremise/PrintCatalyst_RSPI
Origin: https://github.com/SGS-Web-Builder/PrintCatalyst_RSPI.git
Windows reference: C:/Users/sarth/Documents/PrintCatalyst-OnPremise

The Windows reference has active/uncommitted work from other chats. Never assume its HEAD contains the current files. Do not overwrite it or include its Git history/builds/credentials in a blind recursive copy.

## Latest user intent

Keep Windows product independent. Build a separate Pi touchscreen kiosk with the same portal, dashboard, mobile read-only merchant login, Cloudflare/domain/QR, Razorpay and other shop functionality. Customers pay on their phone and see a four-digit pickup code in the portal. Entering it on the kiosk releases the order quickly. No SMS. Latest licence decision: no licence requirement for current Linux/Pi packages; default builds use licensefree.

## Next task

Run target hardware acceptance using packaging/linux/README.md and docs/PREPARATION-CUPS.md. Use a 64-bit Raspberry Pi OS installation. Qualify Pi 3 memory, CUPS paper/duplex settings, real payment, pickup entry, power interruption, ambiguous submissions and automatic desktop login. Do not reimplement the now-wired preparation/dispatch components based on older logs below. Earlier milestone entries are historical. Never change the Windows parent repo or automatically retry an ambiguous printer submission.

## Latest milestone â€” verified payment to portal pickup

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

- Repository root: `node --test test/*.test.mjs` â€” 57 passed.
- runtime: `go test ./...` â€” imported baseline passed (full suite; output in ignored go-baseline.log).
- runtime: `go test ./internal/pickup ./internal/printers/dispatch ./internal/store` â€” passed after adding the core/guard/migration.
- runtime: `go test ./internal/pickup ./internal/printers/dispatch ./internal/store -run 'TestPickup|TestEncrypted|TestKiosk|TestMigrations' -count=1 -timeout=60s` â€” pickup/dispatch tests passed after the final invoice guard and restart/off-mode assertions. The store package had no tests matching this final filter; its full suite passed in the preceding command.
- runtime: `go vet ./internal/pickup ./internal/printers/dispatch` â€” passed.
- runtime with `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`: `go build ./...` â€” passed; compilation only, not a Pi boot/print test.

New tests cover duplicate issuance, unpaid rejection, Preparing without consumption, concurrent claims, persistence across service reconstruction, expiry without payment loss, malformed codes, leading-zero encrypted recovery, ciphertext tampering, automatic/manual/direct dispatch guards, invoice isolation, auto-print-off release and no repeated submission after dispatcher reconstruction.

## Active limitations

A secret-scoped GET pickup endpoint and verified-capture recovery worker now exist. Claim is now exposed only on the separately authenticated 127.0.0.1:8081 listener, with durable quotas. IssueVerified remains privileged; production issuance uses the durable verified-capture queue, never a generic paid state. MarkPrepared requires a real preparation worker; stored digests are not yet verified against immutable print artifacts by the inherited dispatcher. Source-file retention now preserves paid uncollected files; prepared-artifact lifecycle remains pending. Linux protection requires a provisioned systemd credential and real Pi serial; no fallback exists. CUPS/rendering remain absent. The publisher protocol still uses the inherited product identifier and needs Pi issuance qualification. These are implementation tasks, not reasons to disable checks or claim a working kiosk.

## Linux prerequisite continuation â€” 2026-10-03

- Defined Linux protection in LINUX-SECURITY.md before implementing it. Added internal/devicekeys (AES-GCM envelopes, purpose-separated HMAC keys, serial binding, Linux credential-file permission/owner/type/size checks), Linux licence protection, Pi data path, and Linux LibreOffice executable discovery. Existing in-memory, one-time licence checks remain unchanged.
- Added packaging/linux service unit, explicit provisioning script and development setup instructions. No service/key/account was provisioned on this Windows computer. Added .github/workflows/pi-checks.yml for Linux tests and ARM64 compilation; not pushed or run remotely.
- Adapted autodelete age policy to preserve paid/dispatched and nonterminal kiosk orders. Added expired-pickup/failed-pickup/completion regression coverage and updated inherited paid-file expiry expectations to Pi requirements. Order details remain retained.
- Passed: `go test ./internal/devicekeys ./internal/config ./internal/configparser ./internal/licensegate`; `go test ./internal/autodelete -count=1`; `go test ./internal/pickup ./internal/devicekeys ./internal/config ./internal/configparser ./internal/licensegate ./internal/documents`; `go vet ./internal/devicekeys ./internal/licensegate ./internal/autodelete ./internal/documents`.
- Passed with GOOS=linux GOARCH=arm64 CGO_ENABLED=0: `go build ./...`; `go test -c -o ../build/devicekeys-linux-arm64.test ./internal/devicekeys`; changed-package `go vet`. The Linux-only credential permission tests were compiled, **not executed**. WSL is not installed; no Linux environment was installed during this task.
- Passed: `sh -n packaging/linux/provision-device.sh` using Git's shell; `git diff --check`. systemd unit execution/verification, actual Pi identity/permissions, real activation and LibreOffice conversion remain untested.
- Changes remain local and uncommitted. Windows product code/service untouched. Next: payment/pickup integration and trusted kiosk boundary, with Linux/hardware execution required before release.

## Touchscreen continuation â€” 2026-10-03

Implemented localhost touchscreen at http://127.0.0.1:8081/ with responsive CSS, touch/USB keypad, leading-zero preservation, idle input clearing, double-submit blocking, cooldown display, next-customer reset and explicit attendant guidance after ambiguous responses. Added session.go and embedded ui assets, session tests, test/kiosk-screen.test.mjs and root-only packaging/linux/pair-screen.py. Server supports short-lived one-use pairing tickets and 12-hour HttpOnly browser sessions, while retaining strict release request checks. Permanent bearer keys never enter browser assets, URLs or command arguments. Pairing/session state is memory-only and is revoked on service restart; new pairing replaces the prior screen. Pairing attempts use the durable existing quotas. Setup instructions are in packaging/linux/README.md.

Passed: `go test ./internal/kiosk -count=1 -timeout=90s`; `node --test test/kiosk-screen.test.mjs` (2 tests); Linux ARM64 CGO_ENABLED=0 `go build ./...`; Linux ARM64 `go vet ./internal/kiosk`. Tests cover ticket replacement/replay/expiry, session replacement/expiry, no unauthenticated ticket minting, asset proxy rejection, leading zeroes, double submit, input clearing and lost-response handling.

Final full web regression: 61 tests passed (`node --test test/*.test.mjs`). No actual browser visual QA, Pi service boot, pairing helper execution, touchscreen/USB device test or physical print was performed. Manual Chromium startup is documented; automatic desktop startup is not installed. Preparation/CUPS with immutable digest verification remains the next major implementation task. Ordinary orders still remain Preparing until that worker exists. No Windows changes, commit, push or deployment.

## Prepared bundle continuation â€” 2026-10-03

Added `runtime/internal/prepared` and `docs/PREPARED-OUTPUT.md`. This is the storage foundation for the preparation worker, not a finished rendering or CUPS path. The package atomically publishes bounded, canonical manifest/PDF bundles, binds order/line/queue/settings/invoice details into SHA-256 identity, verifies the entire bundle before returning any bytes, rejects corruption and supports idempotent/concurrent preparation. Linux directories must be service-owned/private; Linux file/directory sync and confined filesystem operations are implemented. No runtime wiring was changed and no orders are newly marked ready.

Passed: `go test ./internal/prepared -count=1 -timeout=90s`; final `go test ./internal/prepared ./internal/pickup -count=1 -timeout=90s`; Linux ARM64 CGO_ENABLED=0 `go build ./...`, `go vet ./internal/prepared`, and `go test -c -o ../build/prepared-linux-arm64.test ./internal/prepared`. The Linux-only ownership/permissions/symlink test was compiled, not run. Tests executed on Windows cover tampering, missing/truncated output, changed copies/queues, cross-order identity, restart/reopen, concurrent publication, input validation, no partial output and invoice paper/tray preservation.

Next: implement the trusted renderer/preparation worker and frozen-order transaction, resolve threshold invoice-group timing, then consume verified prepared bytes through CUPS and existing journals. Prepared retention/orphan cleanup and memory budgeting remain pending. Original uploads must never be reopened for printing after prepared bundle verification. No real Pi, renderer or printer test; no Windows-product edits, commit, push or deployment.

## Preparation and CUPS continuation â€” 2026-10-05

Added migration 038 (durable preparation plans and frozen-order/source triggers), dispatcher preparation worker/renderer interface, verified-bundle dispatch path and a local CUPS IPP backend/status adapter. Added preparation.go, prepared_dispatch.go, cups.go and their tests. Updated migration count, made MarkPrepared reject replacement of an already-ready digest, and made printer status errors platform-neutral. See PREPARATION-CUPS.md for contracts and explicit limitations.

The worker freezes data before rendering, verifies source hashes, persists/reuses plans across reconstruction, prepares candidate invoices, publishes/validates output and transactionally marks readiness. Final output options are checked against the frozen plan. Dispatch preflights all documents/required invoices, submits only verified bytes, preserves queue-group invoice thresholds and uses durable submission journals. Tests show five claimed orders above four receive five invoices, below-threshold groups skip invoices, absent original uploads do not prevent prepared dispatch, and corrupt/changed output is blocked before any document submission. Failed rendering leaves the code unclaimed; reviewed invoice retry does not replay completed submissions.

CUPS uses fixed local HTTP/IPP with no proxy/redirect/replay, prepared-PDF-only submission, option fidelity, media-col tray encoding, bounded response parsing, job identity validation and conservative progress mapping. No real CUPS request was sent during this task. No renderer dependency was installed.

Passed:
- `go test ./internal/printers/dispatch ./internal/store ./internal/prepared ./internal/pickup ./internal/autodelete -count=1 -timeout=90s` (all packages passed).
- After final renderer-option guards and digest immutability: `go test ./internal/printers/dispatch ./internal/pickup -run 'TestPrepared|TestReadyDigest|TestCUPS|TestPickup' -count=1 -timeout=90s`.
- After media-col correction: `go test ./internal/printers/dispatch -run 'TestCUPS' -count=1 -timeout=60s`.
- Linux ARM64 CGO_ENABLED=0 `go build ./...` and `go vet ./internal/printers/dispatch ./internal/prepared ./internal/pickup`.

IMPORTANT: main.go has NOT been wired to this worker/backend; its real renderer is still missing. Normal installed orders therefore still remain Preparing. Tests inject synthetic rendered PDFs and mock IPP responses. Next implement/qualify the real Linux renderer (including selected pages, DOCX/images/N-up/Unicode invoice pagination), invoice sheet metadata, route-aware receipts, then wire private storage/worker/CUPS lifecycle together. Discovery/capability qualification, prepared cleanup/orphans/recovery and real Pi/printer/browser tests remain. No production-ready or paper-output claim. Changes are local/uncommitted; no push in this continuation. Windows product untouched.

## Renderer and lifecycle continuation â€” 2026-10-05

This entry supersedes earlier statements that the real renderer and main startup wiring are absent. Work stayed inside the independent Pi repository; the parent Windows product was not modified.

Linux startup now connects private prepared storage, the PDF/image renderer, local CUPS discovery/backend, preparation and dispatch workers, preview rendering and completed-bundle retention. The embedded Python renderer handles selected pages, orientation, 1/2/4-up, grayscale, EXIF-aware images, transparent-image backgrounds and shaped Unicode invoices. Invoice sheet accounting uses actual rendered page counts; invoice text uses frozen printer routes. Completed bundle cleanup retains order/preparation metadata and waits for document and applicable invoice completion.

Resumed the interrupted verification, fixed worker readiness so an unexpectedly exited essential worker cancels its group and reports failure, and added stop/restart regression coverage. Pinned the tested renderer dependencies and documented development-Pi installation and acceptance in packaging/linux/README.md. Added preview regression checks for page selection, PNG dimensions and invalid page requests.

Verified locally:
- 61 JavaScript tests passed (`node --test test/*.test.mjs`).
- Eight Python renderer/preview tests passed, using the repository-local Windows virtualenv and Nirmala font for the Unicode test.
- Go native renderer tests passed with PC_RENDERER_PYTHON explicitly configured; preparation, CUPS, cleanup, pickup, store and prepared-bundle package tests passed.
- Worker stop/restart and prepared cleanup tests passed after the lifecycle change.
- Linux ARM64 CGO_ENABLED=0 `go build ./...` passed, as did `go vet ./internal/printers/... ./internal/prepared ./cmd/print-catalyst-on-premise` under that target.

Remaining: execute the runtime and Linux-only permission tests on ARM64; qualify Python wheels/fonts/LibreOffice and systemd sandbox; browser parity and touchscreen/USB keypad checks; real verified payment, publisher activation and printer acceptance. Orphan artifact cleanup, operational reissue/recovery tools, automatic desktop startup and target-Pi memory budgeting remain separate unfinished work. No actual CUPS/printer submission, Pi boot, production installation, commit or push was performed. Payment alone still does not authorize printing; the trusted physical kiosk claim remains required.
Final verification: full `go test ./...` passed with the real renderer enabled (PC_RENDERER_PYTHON); log: build/pi-final-go.log. `git diff --check` passed (line-ending notices only).

## Test packaging continuation â€” 2026-10-05

Added packaging/linux/build-package.py, install.sh, screen-autostart.py and test_package.py.
The explicit-mode builder cross-compiles an allowlisted ARM64 archive with manifest and
checksums, requires a valid publisher public key for production mode, and labels the
development artifact. No publisher key was invented or private key included. The target
installer checks architecture/checksums, refuses an active application service, preserves
credentials, installs renderer/CUPS prerequisites and the unit, and leaves starting/enabling
the application to the operator. Optional XDG Chromium startup runs under the desktop user;
automatic OS login and automatic screen pairing are deliberately not configured.

Added Store.CleanupAbandoned and startup invocation: only correctly named, incomplete
.preparing directories older than 24 hours are removed. Publication and staging cleanup
are serialized within the store. Published bundles, unknown names and recent staging are
preserved. The service must be the sole process owning this directory. Published orphan
reconciliation remains outstanding; this change never guesses that paid artifacts are disposable.

Passed targeted Go tests for abandoned cleanup, round trips, completed cleanup and worker
restart. Rebuilt build/printcatalyst-pi-arm64-development.tar.gz. Two Python package tests
verify production key requirements, archive allowlist, checksums, LF scripts and ELF ARM64
identity. The installer/autostart have not executed on Linux. Parent Windows files untouched.

Still outstanding: merchant code reissue/recovery UX, published-orphan reconciliation,
actual publisher/Pi activation qualification, browser parity checks and all target-Pi
acceptance. The test archive is not a qualified merchant release. No commit/push/deployment.

Additional recovery work in this continuation: implemented owner-only expired pickup
replacement (ReissueExpired, protected POST owner/orders/{id}/pickup/reissue and dashboard
action). Existing verified pickup, paid status and absence of release/submission history
are required. Expired code is rotated; prepared digest/settings remain; duplicate requests
preserve the new active code. No code is returned by the owner endpoint or printed in logs.
Customer receipt refresh exposes it through the existing secret-scoped portal endpoint.
Kiosk-mode order views now hide the inherited One-Click Print action. Role checks, same-origin
and CSRF guards apply; the physical kiosk remains the only release point.

Targeted tests passed for code rotation, invalidated old code, idempotent recovery,
no automatic release, claimed-order rejection, owner session/CSRF enforcement and existing
owner order endpoints. All 61 web tests pass. Shell syntax checks pass for install.sh and
provision-device.sh using Git sh. More general preparation failure/unfreeze/reconciliation
UX remains unfinished; do not interpret expired-code recovery as permission to replay jobs.

Final checks for this continuation: `go test ./internal/pickup ./internal/prepared
./internal/printers/dispatch -count=1 -timeout=120s` passed (all three packages);
`go test ./internal/localserver ./internal/pickup -run
'TestPickupReissue|TestReissue|TestOwnerOrders' -count=1 -timeout=90s` passed;
`go vet ./internal/pickup ./internal/prepared ./internal/localserver
./internal/printers/dispatch` passed; final package rebuilt and both Python archive
checks passed. `git diff --check` passed with line-ending notices only.


## 2026-10-05 — main-gap fixes

- Migration 040 stores hashed touchscreen pairing bound to the kiosk credential. Re-pairing/credential rotation revokes the old cookie; service restart does not. Browser cookie refreshed on authenticated use; server limit ten years. Pairing tickets remain short-lived and memory-only.
- Claim returns a random, private 24-hour receipt. The screen polls print status without customer/order identifiers, resumes on refresh and resets after completion/assistance. Ambiguous progress requests attendant review.
- Prepared publication and dispatch use one job at a time with manifest preflight and per-submission hash verification. Renderer address-space limit is at most half physical RAM, capped at 1.5 GiB. This is a guard, not proof that every Pi 3 document fits.
- Failed preparation waits for owner Retry document preparation. This does not release printing and refuses already-claimed or journalled jobs. Fixed owner-client allowlist for this action and expired-code reissue.
- Dedicated Chromium profile, startup wait, single launcher lock and browser restart loop. Optional LightDM auto-login helper explicitly enables services; no reboot or job is triggered by configuration.
- Tests: kiosk/pickup/prepared/dispatch passed; 63 browser tests passed. ARM64 development package built and archive checks passed. The full localserver test package also passed (189.9 seconds).
- User authorized publishing these changes to the Raspberry Pi GitHub repository. No deployment or hardware print was performed.


## Licence-free Linux continuation

User requested no activation and localhost dashboard/portal after installation. Default package build now uses `licensefree`; retained optional `--public-key` builds still enforce activation. The free client does not create/load licence credentials or contact the licence server. First-owner setup omits licence key; the dashboard identifies this edition accurately. Existing owner login and shop/printer setup still apply.

Package builder supports `--arch amd64` (Linux PC/VM) and `--arch arm64` (Pi). Installer checks matching architecture, enables/starts CUPS and application, waits for local health and attempts to open the dashboard as the desktop user. Headless/SSH installs print links instead. Linux PC encryption binds to /etc/machine-id when no Pi serial exists; Pi binding stays unchanged. Back up the machine identity with credentials/data; regenerating it prevents decrypting old secrets.

Tests: licence-free startup/release and Linux identity tests passed; all 63 JavaScript tests passed; shell syntax checks passed. Both licence-free Linux architectures cross-compiled. Linux systemd/desktop/browser launch and actual printer output still need testing on Linux hardware/VM. Windows parent untouched.


## 2026-10-12 - systemd credential startup compatibility

User WSL Ubuntu logs confirmed LoadCredential delivers root:root 0440, 32-byte regular files. The old owner-only mode check rejected them before pickup startup. The loader now permits that exact read-only delivery mode only when the containing directory matches CREDENTIALS_DIRECTORY under /run/credentials, is not a symlink, is root/service-owned, and excludes other access/group write. Ordinary credential files stay owner-only; file size, ownership, type and no-symlink checks remain. Existing keys/data are preserved. Portable policy regression tests pass; AMD64/ARM64 rebuild and package checks run. Actual restart on the user laptop remains the next validation; no claim of successful WSL service startup yet.

## 2026-10-19 — touchscreen promotions and OTP layout

Changed only Pi kiosk UI: index.html, kiosk.css, new ads.js, kiosk.js keyboard Enter handling, and session.go embedded asset allowlist. Landscape screen has a local promotional carousel beside the existing four-digit pickup keypad; portrait stacks the panels. Three explicitly labelled sample promotions rotate every ten seconds, pause during code entry/pairing, and have manual slide/pause controls; reduced-motion preference starts paused. No external ad requests or dependencies were added to the release flow. Enter on a focused button/summary keeps its native action instead of triggering release.

Validation: node --test test/kiosk-screen.test.mjs test/kiosk-ads.test.mjs passed four tests; go test ./internal/kiosk -count=1 -timeout=90s passed (9.693s); git diff --check passed. Visually inspected local browser preview; screenshot build/kiosk-ads-preview.png. Preview static server on Windows loopback http://127.0.0.1:18083/ serves actual UI assets but has no payment/claim backend and cannot print. Sandbox loopback restriction required an elevated preview process. Existing unrelated licence-free/device-key changes preserved.

Ad content is currently authored in embedded HTML, not a merchant upload/scheduling feature. No image/video uploader, remote advertising service, Pi deployment, physical touchscreen acceptance or print-speed benchmark is claimed. Next: replace samples with merchant creatives and rebuild/install ARM64 when requested. No commit/push performed.

## 2026-10-19 — full-screen image/video ads with OTP overlay

User superseded the split-panel design: 18.5-inch touchscreen, no header/footer, OTP floats over ads. Replaced kiosk HTML/CSS and ad player accordingly. Local JPEG/PNG/WebP and MP4/WebM playlist is read from /var/lib/printcatalyst-kiosk/ads, name ordered, max 50 files and 250 MiB/file. Images advance after ten seconds; muted videos advance on ended with ten-minute watchdog. Reduced-motion starts paused. Media failures do not affect claim handling; empty/unplayable playlist shows the sample background. Ads continue behind OTP; the earlier pause-on-entry design is superseded.

Added runtime/internal/kiosk/ads.go and ads_test.go; server.go routes private read-only media, OpenRoot confines reads and ServeContent supports range requests; session.go CSP allows same-origin image/media. No upload or merchant bypass endpoint. tools/preview-kiosk.py runs a UI-only loopback preview, with optional media from build/ads. Documented operations and codec/performance limitations in docs/TOUCHSCREEN_ADS.md.

Validation: six focused JavaScript tests passed (image/video transitions, failed/external media, pause, existing release protections); go test ./internal/kiosk -count=1 -timeout=90s passed (11.162s). Browser inspected 1366x768 fallback layout, and checked 1920x1080 has no overflow with the card fully in bounds and no header/footer. Saved build/kiosk-overlay-preview.png. Preview restarted at http://127.0.0.1:18083/. No actual customer media provided; real Pi codec decoding, touchscreen sizing and concurrent video/print performance are not hardware-qualified. No Pi installation, ARM64 rebuild, commit or push performed. Existing unrelated changes preserved.

## 2026-10-19 — remove customer-facing setup and pause controls

Removed the complete pairing disclosure/form and ad pause button from the touchscreen HTML, plus their JavaScript handlers. Unpaired screens still fail closed on the existing backend and show a brief attendant-help message; no authentication bypass was introduced. Ads auto-play with no customer playback controls and stop while the tab is hidden. No ad-upload workflow added, pending user instructions. Existing screen pairing cookies are preserved; a separate administrator provisioning UI remains needed for fresh browsers now that pairing is absent from the customer screen.

Six focused kiosk/media JavaScript tests passed, including visibility pause/resume and existing release safeguards. Refreshed localhost:18083 and confirmed the pairing form and pause button are absent and keypad remains available. Changes are local only; no Pi deployment or push.

## 2026-10-19 - remote ad management completion

Completed the existing unfinished ad upload/scheduling work. Owner-only /ads-admin/ supports campaign durations, recurring windows, timezone and one default ad; uploads remain disabled until saved. Remote monitoring remains read-only. Limits: 80 MiB/file, 50 files / 1 GiB. No deletion UI in this iteration.

Connected the isolated development preview to real kiosk assets/playlist/media handlers, sharing its disposable database at 127.0.0.1:18085. /preview/ opens management and /screen/ opens touchscreen. No print APIs or workers are mounted; rejecting Claimer is defence in depth. Removed broken zero-byte seeded campaign. Added narrow-screen filename wrapping and guarded post-upload editor selection. TOUCHSCREEN_ADS.md includes preview and merchant steps.

Validation: all 67 web tests passed; ads-admin.mjs syntax check passed. go test ./internal/advertising ./internal/kiosk ./internal/localserver -run 'Ad|Campaign|Schedule' -count=1 -timeout=120s passed outside sandbox (initial sandbox run failed Windows temporary database permission protection). Preview build succeeded. HTTP smoke test uploaded a disposable PNG, enabled default, verified production playlist and exact media bytes; claim API returned 404. Browser verified library/settings save and touchscreen. Disabled test image afterwards. Preview is running for user. git diff --check passed. No Pi deployment, live Cloudflare upload, hardware codec/performance qualification, commit or push. Other chats' changes preserved.

## 2026-10-24 - debug audit

User requested bug discovery. Recorded four findings with impact, source locations and fix directions in docs/DEBUG-AUDIT-2026-10-24.md: fresh-screen pairing workflow missing, ad upload can consume disk reserve, scheduled ads remain on fallback after transient failure, invoice review omitted from touchscreen assistance status. Reproduced ad recovery with actual player source in a Node VM; other findings traced in code without destructive disk or hardware tests. All 67 web tests and Go kiosk/pickup/payments/dispatch/advertising suites passed (outside Windows sandbox for database ACL access). No implementation changes, commit, push or deployment. Existing unfinished work preserved.

## 2026-10-24 - four audit findings fixed

Implemented a dedicated loopback-only /admin/pair/ page and external pair.js; permanent credentials remain in the root helper, and existing one-use ticket/session checks are unchanged. Helper and Linux instructions now direct the administrator to this page in the kiosk browser profile; customer screen remains free of pairing controls. Updated obsolete restart/session wording.

localfiles.WriteAtomic now uses a reserve-enforcing writer: free space is checked before every write, a cumulative initial budget is consumed, and capacity-check/write operations are serialized across localfiles writers without holding the lock during network reads. Failed uploads retain atomic cleanup. This prevents an upload from knowingly spending the 64 MiB reserve; unrelated processes/renderers can still consume disk space independently, so this is not an OS filesystem quota.

Ad player retries failed scheduled media after a 30-second backoff even after default playback succeeds; identical five-second playlist refreshes do not constantly restart fallback. Receipt progress now includes separator invoice review when selecting assistance.

Validation: all 68 JavaScript tests passed, including scheduled-media recovery; pair.js syntax and git diff --check passed. Go targeted localfiles/kiosk/localserver tests passed using -run 'Reserve|Ad|Pair|Session|Progress|InvoiceReview|WriteAtomic' -count=1 -timeout=120s. Complete localfiles suite passed afterwards, including cumulative chunk accounting. Added regressions for fresh administrator page loopback restriction, invoice review assistance, oversized/changing-capacity upload cleanup. Existing pairing tests validate ticket/session lifecycle. Tests ran outside Windows sandbox for ACL support. No Pi deployment, physical printer check, commit or push. Existing unrelated changes preserved.

## 2026-10-24 - CUPS discovery protocol repair from Pi logs

User's Canon test job 3 finished in CUPS with filter/backend exit success; physical paper output not explicitly confirmed. Dashboard showed no capabilities and CUPS logged Invalid group tag at the eight-second discovery interval. Fixed discovery request encoding: charset/language value tags and per-value tags with empty continuation names. Replaced the broken response value scanner with length-delimited decoding, repeated values, numeric/boolean/range decoding and structural collection skipping. Collections remain unsupported as capabilities themselves; media-supported remains the paper source. Corrected Pi dashboard's Windows-driver wording to CUPS.

Enabled platform-independent IPP tests on Windows and added independent wire traversal, real-format multivalue/collection/scalar fixtures and truncated-response checks. go -C runtime test ./internal/printers/discover -count=1 -timeout=90s passed; all 68 web tests passed; git diff --check passed. Rebuilding licence-free ARM64 archive for user transfer. No remote installation or new physical print attempt performed. Existing unrelated work preserved.

ARM64 rebuild completed successfully outside the Windows sandbox after sandbox denied writing the temporary executable. Verified archive platform/mode and every manifest payload SHA256. Updated build/printcatalyst-pi-arm64-license-free.tar.gz is ready for transfer; not yet installed on the user's Pi.

## 2026-10-24 — Canon CUPS test print and missing price rows
- Removed inherited Windows-only test-print restrictions from printer_setup.go and printer-setup.mjs; owner authentication and actual backend queue membership still apply.
- normalize.go now derives dimensions from PWG media names, reads legacy color-supported, defaults omitted IPP sides support to simplex, and retains collection-only media entries. Discovery explicitly requests color-supported. Unknown colour information remains unavailable rather than guessed.
- Updated Pi printer help text and dispatch errors. Added legacy_cups_test.go and CUPS test-button/pricing regression in test/printer-setup.test.mjs.
- Validation: printer, discovery and dispatch Go packages passed; all 69 web tests passed. Broad localserver suite exceeded its 120-second total timeout during unrelated passport fixture password hashing; not a passing full-suite run. Focused printer/pricing API check recorded below.
- Rebuilt build/printcatalyst-pi-arm64-license-free.tar.gz using python packaging/linux/build-package.py --license-free --arch arm64; all final manifest SHA256 payload hashes verified.
- Not deployed or physically tested here. User must copy new archive to Pi, install with no active print jobs, hard-refresh dashboard, print A4 test and confirm actual output before enabling. Capability changes may invalidate previous confirmations. Driver/CUPS rejection must be diagnosed from the actual returned error; never blindly retry ambiguous submission.
- Focused validation passed: go -C runtime test ./internal/localserver -run "Printer|Pricing|Paper" -count=1 -timeout=120s (32.962s).

## 2026-10-24 — Pricing setup error and generated test-page latency
User confirms Canon test sheet physically printed. Pricing screenshot shows generic database error; missing business_profile reproduces this in GridBook. Map missing profile to ErrSetup (409 actionable guidance), and add pricing-stage server logging for other failures. Device database not inspected, so missing profile is a candidate, not a confirmed device diagnosis.
Internal generated one-page monochrome test PDFs now submit as final-size vector PDFs without Python rasterization or waiting for the customer renderer slot. Customer preparation and pickup guards unchanged. No Pi latency measurement yet, and no microsecond hardware claim. Updated tunnel instructions from Windows to Linux ARM64.
Tests passed: go -C runtime test ./internal/pricing ./internal/printers/dispatch ./internal/localserver -run 'Grid|Pricing|PaperTest|GeneratedPaper' -count=1 -timeout=120s; node --test test/printer-setup.test.mjs test/pricing-grid.test.mjs (3 tests). ARM64 license-free archive rebuilt; manifest payload hashes verified. Not deployed. User should save complete business profile/currency, open Prices again, and provide pricing grid journal entries if it still fails. Local portal origin http://192.168.169.68:8080 with /portal/ target and empty shop route.

## 2026-10-24 — Initial pricing workflow and network/tunnel audit
- Reproduced the screenshot's first-printer-override failure: SaveGrid requires an existing shared book. The UI now keeps first-time setup on shared rates and offers printer overrides only after shared entries exist. The API returns actionable setup guidance rather than generic pricing-not-configured. Regression verifies shared A4 save makes production LocalReadiness true.
- Diagnosed LAN refusal: printcatalyst-kiosk.service forces PC_BIND_HOST=127.0.0.1, overriding saveDirectOrigin's config file. Automatic approval review rejected broad all-interface binding; explicitly requested user approval. No binding/service mutation was applied; archive verified to retain loopback. Cloudflare can still route to http://127.0.0.1:8080, with 8081 kept private.
- Reviewed origin identity verification, runtime origin application, signed Razorpay webhook route, duplicate handling and pickup guards. Added docs/PI-SETUP-CHECKS.md with exact shared-pricing/local-portal/Cloudflare/Test-Mode/pickup acceptance sequence. No live DNS/payment account operations or Pi deployment performed.
- All 70 JavaScript tests passed; focused pricing-readiness regression passed. Broad Go audit started with: go -C runtime test ./internal/pricing ./internal/provisioning ./internal/tunnel ./internal/payments/... ./internal/localserver ./internal/kiosk ./internal/printers/dispatch ./internal/config -count=1 -timeout=10m. Final results recorded below.
- Rebuilt ARM64 license-free archive and verified all manifest hashes. Existing unrelated changes preserved.

## 2026-10-24 — resumed pricing/tunnel/payment verification completed

Continued from the user's pasted interrupted chat. Inspected current changes and retained the completed shared-pricing fix; no further production code changes were necessary in this continuation. The unresolved broad verification is now complete.

- `node --test test/*.test.mjs`: all 70 tests passed; build/debug-web-2026-10-24.log.
- `go test ./internal/pricing ./internal/provisioning ./internal/tunnel ./internal/payments/... ./internal/localserver ./internal/kiosk ./internal/printers/dispatch ./internal/config -count=1 -timeout=10m`: all nine package results passed. Full localserver took 218.034s; dispatch 82.285s. Log: build/debug-audit-final-2026-10-24.log.
- Initial sandbox attempt failed on temporary SQLite file permissions; reran with normal filesystem/network permissions. That environmental failure is not counted as a product test failure or hidden from this record.
- `python packaging/linux/test_package.py`: four checks passed, including archive payload hashes and platform identity. Existing ARM64 licence-free archive retained; no source-code change required another rebuild.
- `git diff --check`: passed (line-ending notices only).

No claim that every possible issue is eliminated: device configuration, live Cloudflare domain identity/routing, Razorpay test payment/webhook delivery, and physical pickup printing still require the acceptance sequence in docs/PI-SETUP-CHECKS.md. Listener remains 127.0.0.1; previous all-interface exposure approval was not supplied and no attempt was made to bypass that restriction. Cloudflare loopback routing remains supported. No Pi deployment, real charge, commit or push occurred.

## 2026-10-24 — shared colour and back-to-back pricing

User installed previous package successfully but expected both colour/B&W and single-/double-sided prices. Existing grid intentionally hid unsupported printer combinations; this was not evidence that SQLite failed to persist rates. Shared pricing now offers both colours and all three side modes for each enabled paper size, independent of currently available colour/duplex hardware. Per-printer overrides and customer eligibility remain capability-limited; no manual duplex or colour capability is invented. Long-/short-edge duplex remain separate existing price keys (six rows per paper). Existing prices are not auto-filled with invented amounts.

Save reload errors are no longer overwritten by a success banner. Successful save reports the database reload. Repaired mixed UTF-8/Windows punctuation encoding in pricing-grid.mjs while editing it.

Regression TestSharedColourDuplexRatesPersistWithMonoQueue saves six distinct rates and reads them through a fresh pricing service, verifies actual queue capabilities remain unchanged, and rejects an unsupported printer override. Targeted pricing/localserver tests passed (15.240s/21.446s). Updated old web expectation to test printer-specific scope; all 70 web tests pass. ARM64 licence-free archive rebuilt; four packaging tests passed. No device deployment or live quote/print performed. User should transfer/install the newly rebuilt archive, hard-refresh, select shared rates and save desired amounts.

## 2026-10-25 — licence-free business setup could not save prices

Confirmed code defect behind the reported pricing setup blocker: owner/business setup records their local gates without a licence gate, but pricing.Save still demanded all three legacy prerequisites and used strictly ordered CompleteGateInTx. Existing fixtures precompleted the licence gate and masked this fresh-install path.

Pricing now requires owner/business prerequisites in licensefree builds and records its pricing completion in the same database transaction without fabricating activation. Optional licensed builds retain their existing prerequisite and gate ordering. Existing pricing.updated audit remains transactional. No portal readiness or printer eligibility bypass.

Added licensefree_test.go covering actual owner creation, business save, price persistence, readiness advancing to printer, and no synthetic licence record. Fresh regression passed (2.094s). Licence-free pricing/localserver filtered Pricing|Shared|Portal checks passed (15.104s/45.899s); complete default-build pricing package passed (34.554s). Rebuilt ARM64 licence-free archive, package tests passed (4). No Pi deployment or physical print test. User must transfer/install new archive and save desired shared rates again; rejected previous saves cannot be reconstructed. Other chats' existing changes preserved.

## 2026-10-30 — QR/payment-return/OTP/print audit
Read AGENTS and current handoff, verified independent Pi repository, preserved prior changes and unapproved loopback binding. Audited portal receipt persistence and return URL, signed payment verification/reconciliation, pickup issuance/claim, prepared documents and dispatcher journals.

Changes: portal prevents hosted-payment navigation/retry when receipt cannot be saved, tries a compact recovery receipt, preserves draft on failure, and gives explicit return-to-portal/4-digit-code instructions. Kiosk sends a nonblocking notification only after successful durable Claim; dispatcher wakes immediately with coalescing and retains polling fallback. Customer rendering and payment/claim/hash/idempotency checks unchanged. No promise of physical millisecond printing.

Validation: node --test test/*.test.mjs passed 73 tests. ARM64 licence-free build and final archive manifest hashes passed. Targeted Wake|KioskPreparingAndClaim tests passed. Full `go -C runtime test -tags licensefree ./... -count=1 -timeout=10m` ran every package: payment/Razorpay, pickup, preparation, dispatcher, pricing, tunnel and remaining non-failing packages passed. Initial failures: licence activation Windows tests ran in licensefree and expected activation keys; localserver licence test expected 402 in disabled mode; store test expected 39 migrations instead of existing 41. Corrected test build constraints/expected disabled-mode status/migration count. Both optional licensed and licence-free licensegate package reruns passed; other failing tests rerun separately (results below). Initial full-run failure remains in build/audit-2026-10-30.log; do not represent it as a single clean all-package pass.

docs/OTP-FLOW-AUDIT-2026-10-30.md explains scope and hardware acceptance. No deployment, live Razorpay/Cloudflare verification, physical latency measurement, commit or push. Payment return recovery requires same browser/origin; switching browsers or losing session storage after departure remains a limitation. Keep service 8081 private and LAN binding unchanged.
Final affected-test reruns passed: licence-free store/localserver OpenIsIdempotent|SoftwareLicense|ProductionMissingLicense (1.936s/3.212s), default-build localserver SoftwareLicense|ProductionMissingLicense (3.588s). No production changes after final package build; later edits were tests/documentation only.

## 2026-10-30 — continued customer payment notifications and same-day OTP

Continued interrupted idle chat Acc C (01a0dcc0-90b1-7ac0-8a9a-cd198a86a74e); its production changes were present but lacked dedicated notification/day-boundary tests and final packaging. Preserved all prior unrelated changes. Completed optional default-selected browser push, authorized per-order subscription routes, durable VAPID/subscription migration 042 and independent worker integration. Notification includes encrypted order recovery capability, not lock-screen OTP. Google/Mozilla push endpoints only; public-IP dialing and no redirects. Browser permission/unsupported/synchronous error paths do not reject checkout, and selection is rechecked before attaching a subscription. Existing payment receipt storage guard remains.

Found and fixed inconsistent legacy-day handling: portal ViewForOrder and owner ReissueExpired now agree with kiosk Claim/IssueVerified on the created-day boundary. Added midnight and DST regressions, legacy rolling-expiry recovery, reused-code rejection, VAPID persistence, endpoint/key validation, paid verified active-only delivery, retry delay/success deduplication/unsubscribe, HTTP order-token/same-origin authorization, consent/default selection, notification click and fragment recovery tests. Portal states midnight expiry explicitly. No new Razorpay or push request in kiosk release; existing dispatcher wake retained.

Validation: all 79 web tests passed; focused final portal/notification rerun passed (24). Complete licence-free webnotify/pickup/store packages passed (3.018s/7.287s/13.436s). Complete licence-free localserver/payments/payments-razorpay/kiosk/dispatch passed (213.963s/61.234s/0.891s/25.645s/74.319s), log build/notifications-regression-2026-10-30.log. ARM64 licence-free archive rebuilt after final UI notice; four final packaging checks passed. Scoped diff --check passed (line-ending notices); unrelated earlier pricing-grid CRCRLF whitespace remains outside this change.

Added docs/CUSTOMER-PAYMENT-NOTIFICATIONS.md with implementation limits and HTTPS Android/Razorpay Test Mode acceptance steps. No Pi deployment, real push-provider delivery, live payment, physical latency claim, commit or push. Notifications require HTTPS, browser support and permission; old Android/background restrictions may prevent delivery. Website recovery remains primary. Same-day capacity is at most 10,000 reserved four-digit codes; owner reissue can recover expired unprinted paid orders. Hardware and cloud acceptance remain outstanding.

## 2026-10-30 — resumed upload-to-print audit completed

Continued the interrupted code-only Cloudflare/portal/payment/pickup audit. Confirmed independent Pi Git root and origin and inspected the two existing intake fixes: portal uploads one file per request and persists each completed file; documents.Save uses protected WriteAtomic. No additional production changes were needed. Windows sources and network bindings were not changed.

Verified existing evidence (not rerun in this continuation): build/full-flow-audit-2026-10-30.log records the passing full licence-free Go baseline, begun before intake edits; build/intake-audit-final.log records passing post-edit documents/localserver tests (2.732s/39.012s); build/web-flow-audit-final.log records 80 passed, zero failed. Source timestamps precede the post-edit logs.

Fresh completion checks:
- From runtime/internal/printers/dispatch/renderer, with PC_TEST_INVOICE_FONT=C:/Windows/Fonts/Nirmala.ttc: ../../../../../build/renderer-venv/Scripts/python.exe -m unittest test_render.py — 8 passed.
- git diff --check -- runtime/internal/documents/documents.go runtime/internal/localserver/web/portal/portal.mjs test/portal-checkout.test.mjs — passed; line-ending warning only.
- python packaging/linux/build-package.py --license-free --arch arm64 — passed outside sandbox after the sandbox denied writing the temporary compiler output. No product compile error; the initial environment failure is recorded here.
- python packaging/linux/test_package.py — 4 passed, including package architecture/mode and payload checksums.
- Rebuilt ARM64 archive SHA256: 5D788D1F8A798F0C5348C6FE61DB5F06826CCF44378C2E57B998F294A5666D9A.

Deliverable: build/printcatalyst-pi-arm64-license-free.tar.gz. The amd64 archive is older and was not rebuilt in this continuation. Updated this handoff only; preserved unrelated unfinished source changes. No commit, push, installation, live Cloudflare request, payment or physical printer test. Cloudflare is not configured per the user. Remaining acceptance requires the configured domain and target Pi/printer; code checks do not establish physical output or latency. See docs/FULL-FLOW-AUDIT-2026-10-30.md.

## 2026-10-30 — paid pickup / partial-print preservation fixes

User requested a real-world debug audit for paid/OTP-released orders that print only one page and lose documents. Reproduced premature deletion of a completed first document in a partial order, legacy CUPS-completed source deletion without physical confirmation, and age deletion of submitted pending checkout files. Fixed order-wide source retention including invoices, CUPS completion/review and legacy-row handling, prepared-bundle retention, submitted-order age protection and transactional cleanup/checkout eligibility rechecks. Added renderer three-page content verification and partial-bundle restart/explicit retry/no-code-replay regressions. Merchant confirmation now gates CUPS cleanup; automatic scheduler completion is not sufficient. Dashboard footnote explains physical confirmation and uncertain retries.

Changed autodelete.go/tests and new partial_output_test.go; dispatch cups.go/cups_test.go, progress.go, prepared_cleanup.go and new partial_output_test.go; renderer/test_render.py; localserver portal.go, new checkout_retention_test.go and web/order-queue.mjs. Existing unrelated modifications preserved. Details and limitations in docs/PARTIAL-PRINT-RETENTION-AUDIT-2026-10-30.md.

Validation: initial new synthetic retention regression reproduced 1/2/2 deleted files before fixes. Broad Go audit passed dispatch/orders/kiosk/pickup/payments/Razorpay/localserver but two old retention expectations failed (they expected submitted pending/failed files to expire). Updated to requested preservation policy. Final complete autodelete/dispatch packages passed (28.343s/66.855s), build/partial-output-final.log. Subsequent final PartialOrder|Checkout|PortalDelete|PortalUpload checks passed (5.785s/14.640s), build/partial-output-checkout.log. All 80 web tests and 9 Python renderer tests passed; scoped diff check passed. SQLite tests needed execution outside sandbox after Windows ACL denial. Exact commands recorded in audit document.

Rebuilt ARM64 licence-free archive; 4 packaging checks passed. SHA256 D32515AFBC3849703678F2481C2F96DF1EF125E7EC17AF787D2ADA9D2FC5C8E7. No Windows edits, live Cloudflare/payment, customer data access, physical print, deployment, commit or push. A physical first-page-only cause remains unproven without device evidence. All submitted unresolved orders now retain files and can consume disk until resolved; existing reserve checks protect new intake. Explicit retry of an uncertain document can repeat pages already printed and still requires inspecting queue/output; no automatic missing-page recovery. Earlier deleted files cannot be recovered by this update.

## 2026-10-30 — enter OTP once, wait for preparation, release automatically

User explicitly requested Processing your file when a valid OTP is entered before readiness, followed by printing without requiring the customer to enter the OTP again. Implemented a durable waiting authorization separate from the actual prepared release. Migration 043 stores the order plus pickup code hash/generation; no plaintext OTP is added. Claim now persists this wait and returns the order with ErrPreparing. Trusted kiosk responds 202/preparing with a private progress receipt. Touchscreen locks input and polls only that receipt, showing Processing your file, then printer queue/printing status. Existing sessionStorage receipt restoration applies on refresh; polling does not consume the guess budget.

Preparation commit signals a nonblocking notification. A pickup waiting worker rechecks matching pickup generation, paid state, expiry and ready digest, atomically claims through the existing claim transaction and wakes dispatch. One-second recovery fallback handles restart/missed signals. Only a previously entered valid physical OTP can authorize this path. Reissued/expired/unpaid/preparation-failed orders do not release; preparation failure/expiry is shown as assistance. No physical-print milliseconds guarantee. An entered wait remains authorized even if the customer leaves the touchscreen; release after readiness is intentional. Existing midnight expiry remains enforced. The previously documented lost initial response/receipt limitation is not resolved by this change.

Files: migration 043_kiosk_waiting_release.sql; pickup service.go and new waiting.go/waiting_test.go; dispatch config/preparation callback and partial_output_test.go; main worker wiring; kiosk server/progress/UI and new waiting_test.go; existing kiosk test expectation; store migration count; test/kiosk-screen.test.mjs. Preserved unrelated changes and prior document-retention protection.

Validation: node --test test/*.test.mjs — 81 passed (build/waiting-otp-web.log). go -C runtime test -tags licensefree ./internal/pickup ./internal/kiosk ./internal/printers/dispatch ./internal/store -count=1 -timeout=5m — all passed, respectively 22.269s/27.337s/65.387s/20.879s (build/waiting-otp-final.log). Earlier focused Waiting|KioskPreparing|Prepared|OpenIsIdempotent|ProgressReceipt checks also passed. Tests cover early entry, private receipt polling, single attempt despite ten polls, preparation wake, exactly one dispatch, restart, expired/reissued/unpaid/never-entered/failed cases, and no second OTP submission in the UI. Tests used synthetic temporary data outside sandbox for Windows SQLite ACL support. Scoped diff check passed (line-ending notices only).

python packaging/linux/build-package.py --license-free --arch arm64 succeeded; python packaging/linux/test_package.py — 4 passed. Updated build/printcatalyst-pi-arm64-license-free.tar.gz SHA256 4318BF6FF1BA62034506CD20277A46DBE25496DA8B8CCBD921D5347AC3D6B5C7. No Windows product edits, customer document access, live payment, Cloudflare test, physical printing, deployment, commit or push.

## 2026-10-30 — initial remote management dashboard
User authorized implementation of the proposed remote dashboard. Added Pi-hosted /remote/ with isolated owner sessions (migration 044), origin/CSRF protection, live order filters/counts and paper estimates, remote shared/per-printer pricing using the existing pricing-grid validation, and navigation to existing separately authenticated ads management. Read-only /monitor/ and local owner guards remain intact. Extracted pricing handlers without changing SaveGrid behavior. Existing unfinished changes preserved; Windows product untouched.

Validation: 81 existing web tests passed (build/remote-dashboard-web.log); remote JavaScript syntax check passed. Focused licence-free localserver/store/pricing RemoteIs|MonitorIs|OpenIsIdempotent|Pricing|Grid suite passed (26.120s/1.867s/16.658s). Final extended RemoteIs regression passed (3.803s), covering actual price persistence, monitor/remote token isolation both ways, invalid CSRF/origin, expiry and logout. Initial sandbox run could not protect temporary SQLite files; approved outside-sandbox execution passed. A test-only variable redeclaration was corrected before the final passing reruns. ARM64 licence-free package build succeeded and all four package checks passed. No production edits after build; final subsequent edits were tests/docs only.

See docs/REMOTE-DASHBOARD.md for scope and acceptance. This is the initial slice, not the complete proposed fleet dashboard. Remaining: maintenance mode, central offline status, ERS telemetry/API, refunds/settlements, scheduled pricing and safe service controls. No visual browser acceptance, live Cloudflare, Epson hardware, payment, installation, commit or push. Existing package at build/printcatalyst-pi-arm64-license-free.tar.gz now includes this change.

## 2026-10-30 — remote operations, Epson-reference telemetry and fleet service

User requested the remaining dashboard capabilities, using Epson ERS/EM-C800 driver documentation as reference. User confirmed ERS account/API details and central hosting are not ready yet and will be obtained. Continued only in the independent Pi repository; preserved existing unfinished edits. No subagents, Windows product changes, commit or push.

Implemented migration 045; maintenance intake admission and transaction-level checkout recheck; price revision triggers and optimistic checks for shared/per-printer edits; scheduled changes with persistent pending/applying/applied/review/cancelled state; CUPS read-only printer/supply telemetry with negative sentinel and mismatched-array handling; storage capacity; same-session remote ads; currency-separated gross collection totals; paginated account-wide Razorpay settlements; durable idempotent full refunds before physical release; refund/OTP transaction exclusion and explicit customer refund states; preparation retry/expired-code recovery/explicit output confirmation; guarded systemd application restart with durable acknowledgement; signed archive staging; management audit records. Fixed queueState so active printing is not hidden by print_requested. Existing payment verification, immutable preparation, physical OTP authorization and partial-print document retention remain in force.

New standalone fleet service: per-device heartbeat credentials, separate admin key, loopback-only server behind HTTPS, persistent metadata and last-seen/offline state, bounded event history, no customer documents/payment secrets/OTPs in heartbeat payloads. Pi sender uses locally configured HTTPS URL and credential file; no redirects. Added independent amd64/ARM64 fleet build script, service unit and deployment instructions.

Primary files: localserver remote_operations.go, remote_ads.go, remote_commands.go, remote_restart_linux.go/other.go, remote_support.go, remote_updates.go, fleet_heartbeat.go and tests; web remote-operations.mjs plus remote UI/ads/pricing/order queue/portal message updates; pricing revision.go and persistence checks; payments remote_finance.go and razorpay adapter/tests; pickup refund guards; dispatch telemetry.go/tests; localfiles Capacity; internal/fleet server and tests; cmd/fleet-server; migration045; main RunRemote worker; Linux systemd Restart=always; packaging/fleet files. docs/REMOTE-DASHBOARD.md fully replaces the initial-slice status and records sources and limitations.

Verification:
- `go -C runtime test -tags licensefree ./internal/localserver ./internal/payments/... ./internal/pricing ./internal/pickup ./internal/printers/dispatch ./internal/store ./internal/fleet -count=1 -timeout=7m`: all packages passed (build/remote-operations-go.log; localserver 259.851s, payments 79.853s, Razorpay .936s, pricing 63.978s, pickup 33.159s, dispatch 97.122s, store30.366s, fleet.636s). This broad run preceded later support/update/UI additions.
- Final affected reruns passed after those additions. `go -C runtime test -tags licensefree ./internal/localserver ./internal/payments ./internal/fleet -run 'Remote|MaintenanceDoesNot|FleetIsolation' -count=1 -timeout=3m`: 7.025s/4.940s/.511s (build/remote-operations-final-check.log). Includes persisted price conflicts/schedules, maintenance admission versus paid OTP release, refund response loss/stable request ID/status reconciliation/OTP exclusion, already-released refund rejection, customer refunded state, explicit output confirmation, storage readings, signed archive tampering/wrong publisher rejection, fleet authentication/persistence/offline behavior. Intermediate acceptance log covers migration/pricing reruns. Synthetic temporary SQLite tests ran outside the sandbox for Windows file-protection compatibility.
- `node --test test/*.test.mjs`: 82 passed (build/remote-operations-web-final.log); new released-print status regression. Syntax checks for new remote modules and portal passed. No rendered browser visual acceptance performed.
- `python packaging/linux/build-package.py --license-free --arch arm64`: succeeded; `python packaging/linux/test_package.py`: 4 passed.
- `python packaging/fleet/build.py --arch amd64` and `--arch arm64`: succeeded. Independently checked every manifest SHA256 and ELF machine code for both archives.
- Scoped diff whitespace check passed with CRLF conversion notices only.

Artifacts SHA256:
- build/printcatalyst-pi-arm64-license-free.tar.gz: AEBE105FC3CB913D3B205F35C4787AED2BF6DFE1AA71A5264B9EB2177247375D
- build/printcatalyst-fleet-amd64.tar.gz: 75e0b6175fb407e83c50442b0e79a4d7b229bd9319fdaf507aab874aa968e5a5
- build/printcatalyst-fleet-arm64.tar.gz: f57436bc134a42acc0557caa8cd12abdc7a08ab68f663e25ce5df65e3666d42e
Only tests/docs changed after package creation.

Not claimed complete: live ERS integration (credentials/API contract pending), physical Epson supply accuracy/driver qualification, remote printer maintenance commands, automatic privileged software install/rollback, central command relay or fleet-wide pricing/ad pushes, outbound email/SMS alerts. Signed staging requires a locally configured publisher public key and a separately signed archive; installation remains local. Central service must be deployed separately behind HTTPS; tokens/domains are not supplied. Financial API tests used mocks, never real refunds or settlements. No deployment, actual systemd restart, live Cloudflare, live Razorpay, customer data or physical printing occurred.

## 2026-10-30 — live touchscreen, optional OTP AutoPrint and success countdown

User authorized circular live progress, green confirmed success, reset for next customer, and remote OTP/countdown toggles. User clarified countdown means the success-screen countdown, not a pre-print delay. Updated AGENTS product invariant to record explicit owner-authorized automatic release as an alternative to the default physical OTP boundary.

Migration046 adds default-enabled OTP/countdown policy, release_mode provenance, and screen dismissal tracking. Owner-only remote GET/PUT kiosk-flow persists independently patched fields with audit and immediate pickup-worker wake. Countdown-only changes cannot overwrite OTP mode. AutoPrint reuses the atomic pickup/release transaction and dispatcher journal: verified captured payment binding, amount/currency, prepared bundle, current policy, refund exclusion and absence of prior print submission are checked under the control-row write lock. It ignores OTP-only expiry when auto release is explicitly enabled, binds the exact order, and never replays committed releases. OTP mode is enabled by default. Old and new eligible paid orders can auto-release when disabled, disclosed by remote confirmation. Re-enabling affects uncommitted claims; previously authorized work remains authorized.

Private kiosk authenticated event stream checks local progress at250ms, emits changes/heartbeat, reconnects after bounded45s; original receipt polling remains fallback. Automatic screen selection returns only private receipts, tracks dismissal, and preserves session-storage recovery. Spinner/preparing/printing states, green confirmed done, amber assistance, five-second enabled countdown or1.5s success without visible countdown, then appropriate OTP/automatic idle screen. Payment portal and optional push no longer advertise or expose a pickup code in automatic mode. No fabricated progress percentage or physical-completion claim: CUPS scheduler completion still enters review, so green requires existing reliable/merchant-confirmed done evidence.

Files: migration046 and store count46; pickup service/waiting/recovery; kiosk server/progress/new live.go/UI and new live_test.go; remote_operations routes/UI/tests; portal title/status messages; webnotify message; dispatch boundary comment; new payments/automatic_release_test.go; kiosk-screen web tests; AGENTS and remote docs. Preserved prior unfinished edits.

Validation: full `go -C runtime test -tags licensefree ./internal/payments ./internal/pickup ./internal/kiosk ./internal/localserver ./internal/store ./internal/printers/dispatch -count=1 -timeout=8m` passed all six packages (97.028s/32.849s/42.728s/260.757s/31.760s/98.594s), build/kiosk-live-flow-go.log. Subsequent final AutomaticRelease|LiveProgress|Waiting|RemoteOperations|ProgressReceipt|Push checks passed payments/kiosk/webnotify/localserver (11.394s/3.939s/2.458s/5.488s), build/kiosk-live-flow-release-check.log. Last independent-setting patch RemoteOperations test passed3.256s. Tests cover default policy, disabled/re-enabled policy, unverified/unprepared/refund holds, expired internal OTP with explicit AutoPrint, exact-once release after service reconstruction, private stream access, existing waiting claims, receipt expiry, persistent independent settings, no-code portal states, streamed green completion and hidden countdown. Synthetic SQLite tests used approved outside-sandbox execution for Windows ACL compatibility.

Final `node --test test/*.test.mjs`:84 passed, build/kiosk-live-flow-web-final.log. Package build ARM64 licence-free succeeded; four package checks passed. Scoped diff check passed (CRLF notices only). Final archive build/printcatalyst-pi-arm64-license-free.tar.gz SHA256 AB793F79BA4EEE573CF6E7C400ABD369152BEF50CBC8F6B3F4EE39F0F61DD048. Only tests/documentation changed after final package creation. No deployment, commit, push, customer documents, live payments, Cloudflare, physical printing or zero-latency measurement. Hardware acceptance still requires Epson device feedback and unattended-completion qualification; no full physical output was inferred from CUPS state alone.

### 2026-10-30 — local remote dashboard preview
Added tools/preview-remote.mjs, an isolated Node loopback server on 127.0.0.1:18086 using actual remote dashboard assets with clearly labelled synthetic responses. No production database, customer documents, printer workers, payment adapters or external requests. OTP/countdown, maintenance and immediate prices are in-memory demo interactions; operational actions/uploads/scheduling explicitly return preview-only errors. Changes reset on process restart. Run: node tools/preview-remote.mjs. Verified node --check, HTTP 200, kiosk-flow JSON defaults, and browser-rendered dashboard with sample orders/printer/paper stock. Started hidden background process outside sandbox after local socket restrictions; opened and retained visible preview browser tab. Production code/security untouched; no deployment.

### 2026-10-30 — responsive remote dashboard redesign
Reworked remote.html, remote.css, remote.mjs and remote-operations.mjs with original Tabler-inspired styling (reference https://docs.tabler.io/ui/getting-started). Desktop sidebar and mobile six-link navigation, active page markers, hash/back navigation, compact metrics, separate overview/orders/printer/pricing/settings sections, collapsed advanced controls, keyboard focus/skip link. Existing API handlers unchanged. Moved pricing schedule beside pricing; preserved state across navigation, added pricing load for direct hash entry and global operation errors. No external CSS/JS dependencies. Verified module syntax, all 84 web tests (build/remote-ui-web.log), browser pricing navigation/direct reload, mobile layout without horizontal overflow, countdown toggle off/on in isolated demo, desktop sidebar screenshot build/remote-dashboard-redesign.png. Restored browser viewport and retained preview tab. No package rebuild or Pi deployment; prior ARM64 archive does not include this UI revision.

### 2026-10-30 — logo colour palette
Sampled supplied default-logo.jpg using System.Drawing; dominant quantized blue #4860E8, highlight #4870F0. Updated remote.css brand tokens, navigation, buttons, focus rings and neutral blue-tinted accents with charcoal branding. Kept semantic warning/success colours. CSS-only change; no payment/printing changes, package rebuild or deployment.

### 2026-10-30 — cash checkout on Pi without Razorpay
Root cause: portal catalogue forcibly disabled cash whenever pickup was wired, while order creation rejected non-Razorpay methods. Removed both kiosk-only exclusions; explicit owner payment-button settings still apply. Portal now labels Confirm · Cash on counter, hides online notification prompt without Razorpay, and explains when both methods are unavailable. Owner local order queue offers Confirm cash received for unpaid cash orders. New authenticated/CSRF-protected cash-confirm route atomically stores a cash receipt bound to submitted amount/currency/precision and marks paid; rejects draft/cancelled/online, previously submitted print/receipt and captured-intent cases; repeat confirmations are idempotent. Migration047 stores cash separately from gateway evidence and exposes validated authorization view for recovery, automatic claims and screen selection. Existing preparation, OTP, refund and duplicate-release guards retained. Cash does not become a gateway-verified capture. Recovery errors retained and retryable across process restart. AGENTS records user cash authorization.
Validation: new tests cover cash catalogue without Razorpay, actual quote/upload/order submission with kiosk mode, missing auth/CSRF/confirmation, wrong order types, unpaid hold, idempotent receipt, separate evidence, unprepared hold, OTP and AutoPrint replay. Final targeted tests passed 3.735s. Web85/85; package4/4. Full affected Go suite running at this note. Final ARM64 archive SHA256 E3714EE08B5A5A3BA9E5D2A48FBA885809094FC553C08998FBB8998116F884C4. No transfer, deployment, physical print or payment performed. User must install updated archive on Pi; currently installed binary still hides cash.
Full affected licence-free Go suite completed successfully: localserver283.799s, payments115.947s, pickup36.124s, kiosk44.958s, dispatch118.296s, store33.523s (build/cash-full-go.log). Final cash guard test/package were run after the last separator exclusion. All required checks passed.

### 2026-10-30 — immediate OTP for explicitly allowed unpaid cash
User explicitly authorized unpaid cash printing when all_documents policy is enabled. Migration048 adds eligible-order and release-authorization views; unpaid cash requires submitted order and current owner policy, while previously committed releases continue across policy change. Gateway/cash-payment evidence tables remain unchanged: no fake paid/captured state. Recovery issues unpaid cash codes, normal preparation/OTP/waiting/automatic release and dispatch use eligibility, and claim rechecks policy transactionally. Online unpaid remains ineligible. Public order creation issues eligible cash OTP synchronously and includes the pickup view in its response; Order Received renders it immediately and polling recovers lost responses. PaymentPending metadata prevents paid/verified claims in unpaid receipt text. Code reissue/preparation retry retain policy guards. Shop policy label explains OTP vs AutoPrint. Actual Pi logs showed service active but CanonLBP2900 disabled with Unplugged or turned off; no physical diagnosis beyond supplied logs, no commands executed on Pi.
Tests: unpaid policy off/revoked block, online unpaid block, OTP and automatic release, pending_payment preserved, committed release survives policy-off, no replay after restart, prepared-renderer document+invoice dispatch, immediate receipt OTP and frontend unpaid copy. Targeted dispatch/pickup/localserver/payments passed; new receipt and prepared pipeline tests passed localserver3.626s/dispatch12.518s. Final post-copy checks pickup4.277s/localserver9.516s. Web86/86 and package4/4 passed. Full affected suite running at this note. Final ARM64 archive SHA256 D2FF88A26C7E463AB446BCC31118C5D1D0BA3D996ED56D97F7A328CB4A990F5F. No transfer/deployment/customer files/payment/physical print. User must install updated build and restore printer connection/queue.
Full affected Go regression suite passed: localserver286.778s/payments114.991s/pickup39.622s/kiosk50.307s/dispatch122.607s/store36.869s; build/unpaid-full-go.log. Post-final receipt-copy change targeted pickup/localserver tests and package build also passed.

### 2026-10-30 — owner client cash-confirm route omission
User reports no print after OTP and Orders error Only local owner endpoints are permitted. Exact error originates in web/client.mjs allowlist, not server loopback/tunnel guard. cash-confirm route was missing despite backend/UI implementation. Added only that action; owner client tests now cover CSRF forwarding and external/extra-path rejection. Web87/87 and package4/4 passed. Rebuilt ARM64 archive SHA256 B53D18FEB4EEE8CBA3E0642C64CBF236C6FCD6937FCDD769656BF620B559F346. No print-path change in this revision. Pi logs show installer completed and Canon queue now enabled/idle; physical no-print cause remains unverified. User says no error after OTP; need read-only order/preparation/release/submission and CUPS evidence. No SSH access performed and no customer documents read.

### 2026-10-30 — silent OTP entry UX and order notification
User screenshot is customer receipt, not keypad. Prior Pi query proves prepared-ready active pickups, no release rows, no submissions; physical fault not inferred. Found UI gaps: fourth digit previously required extra Print tap, Enter on focused keypad button was excluded, screen-settings polling ignored non-OK/network errors. Changed kiosk.js to schedule one guarded auto-submit150ms after fourth digit, cancel on edits/manual submit/reset, handle Enter consistently, show explicit pairing/service banner, and disable entry when pairing rejected. Pairing remains mandatory; no auth bypass. Updated kiosk page hints for unpaid cash, attendant pairing link, and notification title to Order received — not yet printed with formatted currency instead of raw minor units. Includes prior client.mjs cash-confirm allowlist fix.
Validation:90 web tests passed including auto-submit exactly once, Enter on keypad button, pairing-required visible/no claim, existing lost-response duplicate protection and live progress. Full kiosk Go suite passed20.667s. Four package tests and scoped diff check passed. Final ARM64 archive SHA25695404858DFBF3A5F0AC2FF69088CDF12FFAEAD376914F8ADFD68046E211BAE51. No Pi transfer/deployment or real hardware print. Root cause of the user's particular absent release still not confirmed without paired keypad observation; do not claim physical printing fixed. User needs install, refresh one keypad tab, pair if banner appears, then verify release/submission.


### 2026-10-30 — multi-document screen reset and unpaid dispatch hot loop
User reports actual printing now works, but 7 images with 4 composed produced only 3 singles, early OTP reset, and 3–4 second startup/inter-job gaps. Confirmed code defects: any CUPS review row overrode whole-order progress, and screen assistance auto-dismissed after15s; prepared unpaid orders remained pending_payment and reread/hashed the entire bundle every20ms even after every submission. Fixed progress to defer review-only assistance while unfinished jobs remain, removed automatic assistance reset (keep receipt and poll for recovery/merchant confirmation), removed initial500ms progress delay, and mark prepared order seen only after all submission journals and transaction commit succeed. Durable restart checks still prevent replay. CUPS physical completion remains unproven, so no fabricated green success.
Tests:91 web passed build/batch-print-web.log; targeted kiosk/dispatch/localserver tests passed3.793s/3.600s/2.127s. Full kiosk26.225s and dispatch70.748s passed build/batch-print-go.log. Composition endpoint regression now uses seven images, composes four into one page, retains four documents, tests ownership/retry/failure. Four-line prepared unpaid regression submits all four in a single tick without progress/completion and never replays after tick/restart. These do NOT reproduce or explain the physical missing composed page. Need Pi report before claiming that issue fixed. SSH BatchMode connectivity test timed out; no Pi data read or commands executed there.
Added packaged diagnose-print.py, read-only latest-five metadata (composition indicator/document count/submissions/progress), no contents/OTPs/credentials. Updated package allowlist test to check current ARM64 license-free artifact rather than stale development archive; all4 package tests passed after change. ARM64 package built, SHA256 1A24691F5C2FB46E19CF5375EB57C2C5CEC64A3294B93BA1689311E4B67A910C. No deployment or physical latency validation. Scoped whitespace check passed CRLF notices only. No Windows product changes/commit/push. Remaining: inspect affected Pi order using helper plus lpstat to locate merged PDF loss/CUPS abort, qualify Canon job-gap behavior; do not blindly reprint old order or promise zero mechanical delay.

### 2026-10-30 — between-document latency follow-up
Continued AccD's interrupted delay investigation. Latest supplied Pi report shows merged PDF plus three images all submitted as CUPS jobs9–12, each marked review with CUPS job identity changed; this is not proof of physical output. JobProgress omitted requesting-user-name although submission set printcatalyst-kiosk. Added matching owner in status requests (CUPS job-name is private by default; https://www.cups.org/doc/man-cupsd.conf.html). Identity/queue validation and manual physical completion remain intact; old review rows are not automatically cleared/replayed.
Prepared dispatch previously verified all PDFs then read/hashed each again between submissions. Added bounded16MiB verified-byte cache using exact preflight bytes; uncached jobs still ReadJob with hash verification. Entire bundle must pass preflight. No new submission concurrency, delays, payment changes or retries. Added structured print_preflight/print_submission timing logs (hashed order/line, milliseconds, byte count, accepted boolean; no contents/names/OTPs), and timestamps in read-only diagnostic report. This reduces redundant reads but does not establish the cause of reported3–4s physical gaps or promise elimination. Separate CUPS jobs/Canon driver may still introduce gaps; measure before batching changes.
Validation: go -C runtime test -tags licensefree ./internal/prepared ./internal/printers/dispatch -count=1 -timeout=5m passed1.112s/67.479s; build/dispatch-delay-check.log. Covers cache cap, exact-byte snapshot, uncached tamper, corrupt entire preflight, status owner and existing multi-document/no-replay tests. ARM64 package rebuilt and4package tests passed; scoped whitespace check passed CRLF notices only. Archive SHA256 DD46D3E5C0B25BD4A728DBC75E78162E47F525600F52A269664D7C651457C41C. No transfer/deployment/physical print/live CUPS test performed.
Next: install while idle, test a NEW small multi-document order once, collect sudo journalctl -u printcatalyst-kiosk --since '10 minutes ago' --no-pager -g 'print_preflight|print_submission', sudo python3 diagnose-print.py, lpstat -o. Latest other chat IP192.168.171.155 may change. If submissions are fast but output pauses, inspect Canon/CUPS backend timing rather than bypassing journals or blindly reprinting old orders. Full physical missing-page and latency issues remain open pending these measurements.

### 2026-10-30 — Epson EM-C800 consecutive network handoff
User changed kiosk target to Epson EM-C800 and wants separate documents released promptly, with transport chosen for speed. Researched official Epson EM-C800 specification/user support and OpenPrinting network backend/admin documentation. Crucial distinction: prior Pi trace already accepted three local CUPS jobs within139ms, while later status observations were ~27/50/75s. That trace was Canon USB/CAPT, not an Epson benchmark. CUPS IPP supports waitjob=false and waitprinter=false to hand off consecutive independent jobs without waiting for remote completion/idle. Epson first-page specification is about4.8s mono/5.3s colour, not milliseconds.
Implemented packaged configure-epson.py: read-only probe by default, explicit --apply local root administration, actual EM-C800 model/advertised driverless format check using Get-Printer-Attributes, LAN IPP/IPPS URI validation, same-device/case-insensitive queue protection, stopped application and empty existing queue required. New queues use everywhere; existing queues retain driver/TLS/other URI parameters and are never implicitly resumed. Both backend wait flags false and stop-printer error policy; no raw-PDF bypass, no document merging, no job deletion or automatic retry. Verifies configured URI after lpadmin; failure is reported, no unproven transactional rollback claim. Installer adds cups-ipp-utils and installs helper/diagnostic; archive includes EPSON-EM-C800.md with setup, rollback, measurement/acceptance and official research links.
Removed touchscreen fixed150ms fourth-digit delay; next-event-loop submit remains guarded against duplicates/edits/manual click. Added ten-separate-prepared-lines regression with mixed copies/colour/duplex asserting no JobProgress wait, no post-OTP rendering, independent frozen settings, and no replay after dispatcher reconstruction.
Validation:9 helper tests passed (mock commands/probe output; no real CUPS mutations). Initial sandbox temp-file ACL failure was environmental; approved normal-permission rerun passed. All91 web tests passed (build/epson-web-check.log). Targeted Go TenSeparate|FourPrepared|CUPS|Wake passed5.612s (build/epson-dispatch-check.log). ARM64 license-free package built;4package integrity checks passed. Scoped tracked whitespace checks and Python compilation passed. Final archive SHA25657A4AA5C3FEF8EE46257FB29E4663ECD6B8E3B4E79D34F54F61E73B0A050162F. No source changes after this package except this handoff.
Not deployed, no Epson IP supplied, no hardware benchmark/physical test performed. Helper must be applied after installation, followed by dashboard queue/capability selection; old frozen orders/Canon queue are not rewritten. Millisecond local acceptance is a target for small prepared jobs, not a bound for large transfers, busy/offline printers, driver conversion or physical output. LAN recommended for documented IPP controls, not asserted universally faster than USB. CUPS completion remains physical-output review, especially important with non-waiting remote handoff. Follow docs/EPSON-EM-C800.md for new-order mixed-setting/offline/restart acceptance. No Windows product changes, commit or push.

### 2026-10-30 — 31 October kiosk setup package and source publication
User requested latest Pi GitHub push and complete next-day real-kiosk setup documentation. Added docs/KIOSK-SETUP-2026-10-31.md: existing-card upgrade vs fresh imaging, Pi power/display/USB touch/Ethernet, Windows transfer/current IP and checksum, idle check/private state+key backup, installation/health, Epson driverless profile, local owner/pricing, dedicated Chromium profile pairing and LightDM autostart, HTTPS Cloudflare/QR, Razorpay test-to-live acceptance, ads/remote/ERS boundary, multi-document/restart/offline diagnostics. Guide is included in archive and linked from root/package README and older Canon checks. Installer now also installs pair-screen.py at stable helper path. Corrected known orphan-temp assertion in documents/intake_test.go to strings.HasPrefix; production intake unchanged.
Final validation: full go test -tags licensefree ./... -count=1 -timeout=10m passed (build/kiosk-deployment-go.log); focused post-assertion Intake passed1.333s. Previous latest UI/helper checks:91 web/9 Epson-helper passed. Rebuilt final ARM64 package and4package checks passed. Transfer SHA256 file generated alongside archive. Final archive SHA256 fda6f760989fa1099a5c534697e31b863db4b58cb6e8f4de6dd6287a8ef20c1c. Only this handoff added after final package build.
Git source publication authorized to existing codex/pi-kiosk-gap-fixes branch; remote tip checked at a4b493d before staging. Include accumulated Pi-only source/docs/tests from all sessions; no customer data/credentials/build artifacts. Source inspection found no private-key/GitHub-token patterns in candidate directories. Build directory remains ignored: source push does not itself publish a GitHub binary release. Local archive and sidecar are delivery artifacts. No actual Pi/Epson/touchscreen/Cloudflare/payment deployment performed. Tomorrow requires actual hardware acceptance, not an unconditional millisecond guarantee.
