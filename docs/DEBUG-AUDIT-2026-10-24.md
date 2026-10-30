# Pi debug audit - 2026-10-24

Scope: current Raspberry Pi working tree, including unfinished changes. No production implementation changes. No physical Pi/printer or live tunnel test.

## P1 - Fresh touchscreen cannot complete documented pairing

Evidence: packaging/linux/pair-screen.py:35 prints a ticket and directs the administrator to enter it on the screen. runtime/internal/kiosk/session.go:21 exposes only the customer screen/assets; the pairing UI and corresponding JavaScript submission were removed. server.go:94 still rejects claims without a valid pairing cookie. A new Chromium profile therefore cannot release paid orders through the documented setup flow. Existing paired profiles are unaffected.

Fix direction: a separate local administrator pairing page/workflow, keeping pairing controls off the customer screen and preserving authentication.

## P1 - Ad upload can consume the print/database disk reserve

Evidence: runtime/internal/localfiles/files.go:71 checks only whether free space is already below 64 MiB, then line 101 copies the entire input without reserving its size or rechecking space. ads_admin.go:19 permits 80 MiB uploads. With 100 MiB free, an allowed 80 MiB upload leaves only 20 MiB; with 70 MiB free it can fill the filesystem before failing. Library quota is not a free-space check. This affects the same volume used by SQLite and prepared output.

Fix direction: enforce available space minus a protected reserve during writes, including simultaneous document/render writes; retain atomic cleanup. Finding is based on code-path analysis, not a destructive disk-fill test.

## P2 - Scheduled ads do not recover after fallback succeeds

Evidence: kiosk/ui/ads.js:25 switches playlist to defaults and resets failures; line 27 resets failures after successful load. Line 51 only restores scheduled entries when the playlist response changes or failures is nonzero. A transient media failure followed by a successful default therefore leaves the default indefinitely until a schedule/list change or reload.

Reproduced with the real player source in Node VM: fail scheduled video, load default image, refresh identical playlist. Scheduled video is not retried. Existing player tests stub setInterval and miss this recovery path.

Fix direction: retain the scheduled playlist separately and retry failed scheduled media with bounded backoff while the default continues.

## P2 - Invoice review is shown as ongoing printing

Evidence: orders/print_progress.go:23 includes invoices, but line 41 maps review to processing. kiosk/progress.go:60 overrides review to assistance only for print_submissions, omitting separator_invoices. When documents complete but a CUPS invoice job is cancelled/aborted or its identity needs review, the screen keeps saying documents are printing. Separator polling stops revisiting review jobs, so that state persists until manual intervention without prompting the customer.

Fix direction: include separator invoice review in the receipt status and add a regression for completed documents plus an invoice needing review.

## Checks

- node --test test/*.test.mjs: 67 passed.
- go test ./internal/kiosk ./internal/pickup ./internal/payments ./internal/printers/dispatch ./internal/advertising -count=1 -timeout=180s: all five packages passed. Executed outside Windows sandbox because database ACL protection needs normal temporary-file access.
- Read-only inspection of setup, pairing, payment/release, CUPS progress, storage and advertisement paths.

These passing tests do not cover the bugs above. Hardware rendering, systemd startup and physical print output were not validated. No fixes, commit, push or deployment performed.

## Resolution - 2026-10-24

All four findings have code fixes and regression coverage. See the corresponding HANDOFF.md entry for implementation details and exact checks. The disk reserve is enforced by application upload writers, not an operating-system quota against unrelated processes. Physical Pi deployment/acceptance remains outstanding.
