# Preparation workflow and CUPS adapter

Status (2026-10-05): development implementation now wired into Linux startup, with a real Python PDF/image renderer, CUPS adapter, preview generation and completed-bundle cleanup. Renderer execution was tested on Windows; Linux ARM64 compilation passes. Real Pi/CUPS/printer qualification remains outstanding.

## Implemented workflow

Migration 038 adds durable preparation plans and database triggers. Once preparation starts, order lines, pricing/customer/routing fields and active source-file metadata are frozen. Print/payment progress can still change. Source metadata may be cleaned after completion/cancellation. Freeze records/order details are retained.

`Dispatcher.RunPreparation` polls for active paid pickups, records a frozen plan before rendering, verifies original SHA-256 checksums, reflows tagged image compositions and calls the injected `PreparationRenderer`. Its returned PDF must already contain selected pages, orientation and N-up; final copies, paper, colour, tray and sides are checked against the plan. It publishes/verifies the bundle and commits readiness plus the digest atomically in SQLite. Failures retain the pickup code and retry after 30 seconds. Plans survive reconstruction; ready bundles are not regenerated. An existing ready pickup digest cannot be replaced by MarkPrepared.

Candidate invoices (including their frozen text/logo/paper/tray) are prepared before pickup. Actual invoice yes/no decisions still use the existing waiting-claimed-order queue threshold at dispatch. Thus five claimed orders above threshold four produce five invoices; below-threshold groups do not print the candidate invoices. Newly enabled invoice requirements or changed tray/paper settings that do not match prepared candidates block the whole order before submission. Changes to threshold/enablement are evaluated at queue planning; do not silently rewrite already planned groups.

With `DispatcherConfig.Prepared` set, dispatch requires matching release/preparation digests, verifies the whole bundle, checks exact line membership/current printer routing and all required invoices, then submits the verified bytes. It never reopens an original upload. Journals are persisted before each submission. Interrupted/failed submissions require explicit review. Retrying an invoice does not replay its already submitted documents.

## CUPS backend

`NewCUPSBackend` uses IPP over literal `http://127.0.0.1:631`; no environment proxy, redirects, command shell or automatic submission retries. It accepts only final prepared PDFs. Copies, sides, paper, colour, print-scaling=none, number-up=1 and optional media-col tray are sent with attribute fidelity required. Unsupported/substituted settings, missing job IDs, malformed responses and network uncertainty return failures for the journal to hold for review. Stored job identities contain opaque order/line hashes rather than customer names.

Job queries validate the stored title/order/printer identity before mapping pending, processing, held/stopped, cancelled/aborted and completed states. CUPS completion is spooler evidence, not a hardware paper sensor. Missing/failed status requests block confirmation and never trigger reprinting. Printer names are restricted to local CUPS queue names; arbitrary customer URLs are not accepted.

Protocol references: [IPP model and semantics](https://www.rfc-editor.org/rfc/rfc8011), [IPP encoding and transport](https://www.rfc-editor.org/rfc/rfc8010), [PWG job processing extensions, media-col](https://ftp.pwg.org/pub/pwg/candidates/cs-ippjobext21-20230210-5100.7.pdf). The implementation is original; no reference implementation code was copied.

## Remaining integration and qualification

- Qualify the fixed Python virtualenv, PDFium, fonts and optional LibreOffice conversion on the target ARM64 Linux OS under the systemd sandbox. The real renderer covers page selection, orientation, N-up, grayscale, EXIF images and shaped Unicode invoice pagination; invoice accounting now uses rendered page counts.
- Exercise CUPS discovery/capabilities and physical driver fidelity for every offered paper, tray, colour, duplex and copy option. Unsupported options must be surfaced, never silently downgraded.
- Completed-bundle cleanup is implemented and preserves order metadata. Orphan cleanup, operational recovery/unfreeze/reissue tools and target-Pi memory budgeting remain outstanding.
- Execute real Pi service startup/shutdown, browser parity, payment, pairing and printer acceptance. Tests use mocked IPP transport; no actual CUPS submission or paper output was verified. See packaging/linux/README.md for development prerequisites.