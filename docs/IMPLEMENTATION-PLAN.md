# Implementation plan

Status legend: DONE / NEXT / PLANNED. Do not mark a phase complete from documentation alone.

## 0 — Foundation: DONE

Independent clone and handoff documents; no application import yet.

## 1 — Audited source import: NEXT

Read Windows reference instructions and inspect git status/diff. Copy an explicit allowlist of reusable runtime/web/tests into this repository. Do not copy .git, build, generated archives, php-license-website/private, licences, keys, .env files, live SQLite databases, logs or customer documents. Inventory source files containing embedded secrets before copying. Preserve applicable copyright/licence notices.

Record source commit and working-tree provenance; parent has uncommitted work so HEAD alone is not an exact source snapshot. Create an import manifest of copied file hashes, without secrets. Rename Go module/imports to the new repository and create a separate product manifest. Remove inherited Windows publishing jobs from the kiosk build pipeline. Verify portal/dashboard baseline tests before kiosk changes.

## 2 — Linux/ARM service: PLANNED

Make startup, data paths, one-time activation storage, document conversion and previews work on Linux. Build/test ARM64. Add dedicated systemd service and browser startup; use emulated builds only for limited checks, not physical printer claims.

## 3 — Payment and pickup state: PLANNED

Add migrations for pickup/preparation/release records, idempotent code allocation after verified payment, portal display/recovery, expiry/reissue and brute-force limits. Retain existing payment verification and merchant monitoring. Explicitly disable paid-order auto-dispatch in kiosk mode. Cover duplicate callbacks and concurrent claims.

## 4 — Printer preparation and release: PLANNED

Implement CUPS/IPP capabilities, routing/options, rendering, status mapping and invoice output. Stage prepared jobs without submitting them; release only after valid code claim. Test restart and unknown-outcome reconciliation. Test physical selected-page, duplex, colour and copy correctness.

## 5 — Touchscreen UI: PLANNED

Large four-digit keypad, clear/backspace, accessible contrast, Preparing/Printing/Done/Assistance states, timed reset and no previous-customer data. Owner access requires separate authentication. Portal/dashboard retain familiar design and responsive behaviour.

## 6 — Qualification and independent release: PLANNED

Run docs/TEST-PLAN.md on real target Pi and printers. Produce install/backup/update/recovery instructions and a versioned Linux package. Verify Windows repository files and service configuration are untouched. Release only after payment-to-code-to-paper acceptance.
