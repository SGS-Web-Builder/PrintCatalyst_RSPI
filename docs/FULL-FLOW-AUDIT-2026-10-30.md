# Upload-to-print code audit — 2026-10-30

## Scope and outcome

Reviewed the Pi product's file intake, draft recovery, shared pricing readiness, Cloudflare host/origin handling and domain verification, payment return and capture verification, daily pickup codes, preparation, dispatch and CUPS submission. User confirmed Cloudflare is not yet configured and requested code-only verification. No live domain, payment, Pi or physical printer was accessed.

## Fixed in this audit

1. Multi-file uploads previously put up to ten 50 MiB files in a single multipart request. That can exceed Cloudflare's normal request-size limits even though each file is accepted by the app. The portal now uploads one file per request, persists each successful result, and keeps earlier files if a later connection fails. Retrying the same draft retains the existing server deduplication. Each file still has the 50 MiB limit; this is not byte-level resumable upload. A zone configured below that size or an unusually slow upload can still fail.
2. Document Save wrote directly with os.WriteFile, bypassing localfiles.WriteAtomic and its protected file permissions, fsync/atomic commit and 64 MiB reserve checks. Intake now uses the protected writer. Added a regression that forces insufficient reserve, verifies no document metadata is inserted, then checks successful stored bytes. Multipart temporary storage is separate from final blob storage and is not a filesystem quota.

Cloudflare reference: https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/ . Free/Pro limits are documented as 100 MB and zone administrators can lower them. Splitting the application request avoids the batch-size failure without changing tunnel exposure.

## Existing safeguards checked

- Upload/session tokens and submitted-order checks protect draft mutation; batch rollback and duplicate-file handling remain covered by portal tests.
- Quotes remain server-authoritative and limited to enabled printer capabilities.
- Cloudflare forwarding is trusted only from loopback with the configured public origin; cross-origin/spoofed forwarding is rejected. Domain verification checks installation identity, rather than accepting an arbitrary successful page.
- Payment return is navigation only. Captured gateway evidence, amount, currency and order identity authorize paid pickup issuance.
- Kiosk claim requires a paid, prepared, unused, current-day code. Claim is durable before dispatcher wake; no new Razorpay/push call is added during release.
- Prepared content/digests and printer routing are checked before submission; ambiguous submissions require review rather than blind retries.
- Local listener binding and private kiosk service are unchanged.

## Validation

- Full baseline: go -C runtime test -tags licensefree ./... -count=1 -timeout=10m — all packages passed. Log: build/full-flow-audit-2026-10-30.log. This run began before the intake edits.
- After intake changes: documents/localserver -run 'Intake|Upload|Portal|Cloudflare' — passed (2.732s/39.012s). Log: build/intake-audit-final.log.
- node --test test/*.test.mjs — 80 passed. New test exercises two maximum-size file selections as separate requests and retention of the first after a simulated second-request failure. Initial mock attempted to replace a const UI helper; corrected the test harness, then reran successfully.
- Renderer: build/renderer-venv/Scripts/python.exe -m unittest test_render.py with PC_TEST_INVOICE_FONT=C:/Windows/Fonts/Nirmala.ttc — 8 passed. Default Windows Python lacked pypdfium2; the first venv run lacked the required test-font variable. Both environmental prerequisites were resolved; no renderer production changes.
- Scoped diff --check passed. ARM64 licence-free package rebuilt and final package validation recorded in HANDOFF.

## Remaining acceptance checks

Configure the Cloudflare HTTPS domain to the local 8080 service, verify its identity in the dashboard, then perform a phone upload and Razorpay Test Mode payment/return/notification. Test the touchscreen code once prepared and confirm one physical print. The actual Linux driver, printer warm-up, network throughput and millisecond release latency cannot be established by these Windows-hosted code tests. No claim that every possible defect is eliminated.
