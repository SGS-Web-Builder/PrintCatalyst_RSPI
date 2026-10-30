# Paid pickup and partial-output audit — 2026-10-30

Scope: Pi code and synthetic documents only. No customer files, live payment,
Cloudflare connection, deployment or physical printer were accessed.

## Reproduced defects and fixes

1. Source cleanup independently deleted a completed document while another line
   in its order still needed review. The new two-line regression reproduced one
   deleted recovery file. Cleanup now requires the complete order and all selected
   separator invoices, preserving every source during partial failure.
2. CUPS scheduler state 9 mapped directly to `completed`. That state flowed into
   immediate source and prepared-bundle cleanup without physical page evidence.
   A regression with old CUPS-completed rows reproduced deletion of both sources.
   CUPS completion now maps to review, with explicit instructions to check all
   pages/copies. Existing unconfirmed CUPS completion rows become reviewable during
   monitoring. Both cleanup paths reject legacy CUPS completion unless the
   merchant's existing Confirm printed action has confirmed output.
3. Submitted checkout files older than 24 hours were eligible for deletion while
   payment was still pending, and submitted failed orders could lose recovery
   files. The new pending-checkout regression reproduced two deleted files.
   Age-only expiry now applies to unsubmitted drafts or explicitly cancelled
   orders, not active submitted checkout/failed orders.
4. Cleanup selected candidates before deleting without holding a write reservation;
   checkout also checked document availability before its commit transaction.
   Cleanup now reserves SQLite writes across selection/deletion, limited to 100
   files per pass. Checkout rechecks document ownership and purged status after
   acquiring its write lock and rolls back before creating payment if a quoted
   document became unavailable. A deterministic database-trigger regression
   simulates expiry at checkout and verifies no submitted order, line or payment
   intent is committed.

## Failure scenarios exercised

| Scenario | Result |
|---|---|
| First document finishes; second needs review | Every source retained |
| All document jobs finish; separator invoice fails | Sources and prepared bundle retained |
| CUPS says completed without physical confirmation | Review; no automatic cleanup |
| Old CUPS completion rows after upgrade | Retained and made reviewable; already deleted data cannot be restored |
| Pending checkout older than 24 hours | Files retained for delayed verification/recovery |
| Response lost on second prepared submission | Durable failure; no automatic replay |
| Dispatcher restart after partial submission | Accepted first document not resent |
| Explicit merchant retry after queue/output inspection | Only failed/interrupted submission retried |
| Pickup code entered again after successful claim | No second release |
| Source checksum, bundle, route or invoice mismatch | Existing regression suites reject unsafe dispatch |
| Three distinct source pages, one-up rendering | Three distinct output pages preserved |
| Selected-page/N-up rendering | Existing selection/layout checks pass |
| Merchant confirms all physical output | Source/prepared cleanup permitted; accounting metadata retained |

## Behaviour and limits

The kiosk still releases quickly after a durable valid claim. It does not contact
Razorpay during code entry. It now displays assistance/review when CUPS finishes
without physical confirmation. A merchant uses **Confirm printed · Mark done**
only after checking all pages, copies and invoices. The dashboard explains this.

Retained submitted orders can use disk indefinitely until resolved. The existing
disk reserve rejects new writes rather than silently deleting recovery documents.
Abandoned drafts still expire. Explicit cancellation, confirmation, external file
deletion and disk failure are separate from partial-print protection.

An interrupted submission may already have printed some pages. The existing retry
retries the entire uncertain document, not a selected missing-page subset. It must
remain an explicit operator action after inspecting the queue and output tray.
No exactly-once physical output or automatic page-level recovery is promised.

No generic first-page-only renderer defect was reproduced. A real one-page output
incident still requires the order's saved settings and non-content job diagnostics
on the Pi: CUPS job identity/state/reasons, service logs, driver, requested pages,
copies, paper/duplex/N-up settings and actual sheets delivered. This audit does not
establish the physical cause or recover files already deleted by an earlier build.

## Validation

- Initial new retention regression: all three scenarios failed with actual file
  deletion before fixes. Sandbox first blocked temporary SQLite protection;
  outside-sandbox rerun reproduced the defects.
- `go -C runtime test -tags licensefree ./internal/autodelete ./internal/printers/dispatch ./internal/orders ./internal/kiosk ./internal/pickup ./internal/payments/... ./internal/localserver -count=1 -timeout=10m`:
  dispatch, orders, kiosk, pickup, payments, Razorpay and localserver passed.
  Two old retention expectations failed because they required deleting submitted
  failed/pending orders. Updated those expectations to the preservation policy;
  this initial run is not represented as an all-green run.
- Final complete autodelete and dispatch suites passed (28.343s/66.855s):
  `go -C runtime test -tags licensefree ./internal/autodelete ./internal/printers/dispatch -count=1 -timeout=5m`.
- After final checkout change and extra invoice/source-confirmation tests:
  `go -C runtime test -tags licensefree ./internal/autodelete ./internal/localserver -run 'PartialOrder|Checkout|PortalDelete|PortalUpload' -count=1 -timeout=3m`
  passed (5.785s/14.640s).
- `node --test test/*.test.mjs`: 80 passed.
- Renderer venv `python -m unittest test_render.py`, with
  `PC_TEST_INVOICE_FONT=C:/Windows/Fonts/Nirmala.ttc`: 9 passed.
- Scoped `git diff --check` passed (line-ending notices only).
- `python packaging/linux/build-package.py --license-free --arch arm64` passed.
- `python packaging/linux/test_package.py`: 4 passed.
- Archive SHA256: `D32515AFBC3849703678F2481C2F96DF1EF125E7EC17AF787D2ADA9D2FC5C8E7`.

Logs: `build/partial-output-audit.log`, `build/partial-output-final.log`,
`build/partial-output-checkout.log`, `build/partial-output-web.log`.
Windows product unchanged. No commit, push or deployment.
