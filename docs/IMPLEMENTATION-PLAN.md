# Implementation plan

Status legend: DONE / NEXT / PLANNED. Do not mark a phase complete from documentation alone.

## 0 — Foundation: DONE

Independent clone and handoff documents.

## 1 — Audited source import: DONE

Working-tree provenance is in SOURCE-IMPORT.json. Module identity is independent. The 57 retained web tests and imported Go suite pass. Windows-only source is retained as reference, without Windows packaging/release jobs or publisher private keys.

Read Windows reference instructions and inspect git status/diff. Copy an explicit allowlist of reusable runtime/web/tests into this repository. Do not copy .git, build, generated archives, php-license-website/private, licences, keys, .env files, live SQLite databases, logs or customer documents. Inventory source files containing embedded secrets before copying. Preserve applicable copyright/licence notices.

Record source commit and working-tree provenance; parent has uncommitted work so HEAD alone is not an exact source snapshot. Create an import manifest of copied file hashes, without secrets. Rename Go module/imports to the new repository and create a separate product manifest. Remove inherited Windows publishing jobs from the kiosk build pipeline. Verify portal/dashboard baseline tests before kiosk changes.

## 2 — Linux/ARM service: IN PROGRESS

Linux serial-bound licence-key protection, private service credentials, Pi data path, optional Linux LibreOffice discovery and systemd/provisioning scaffolding are implemented. Linux ARM64 compilation passes. Linux permission tests are compiled but not executed locally; actual systemd/Pi startup and printing/rendering remain unqualified. See LINUX-SECURITY.md and packaging/linux/README.md. Compilation is not runtime qualification.

Make startup, data paths, one-time activation storage, document conversion and previews work on Linux. Build/test ARM64. Add dedicated systemd service and browser startup; use emulated builds only for limited checks, not physical printer claims.

## 3 — Payment and pickup state: IN PROGRESS

Migrations 035–038, encrypted/HMAC pickup service, transactional claims, dispatch guard, startup key derivation, verified capture/reconciliation queue and portal code display/recovery are implemented and tested. Retention protects paid/uncollected files. Cash checkout is disabled in kiosk mode without changing saved settings. A separate authenticated loopback release API now has durable global/per-kiosk throttling. Manual touchscreen pairing/session setup is implemented. Automatic desktop startup, merchant reissue and complete hardware integration remain pending. See KIOSK-ENDPOINT.md.

Add migrations for pickup/preparation/release records, idempotent code allocation after verified payment, portal display/recovery, expiry/reissue and brute-force limits. Retain existing payment verification and merchant monitoring. Explicitly disable paid-order auto-dispatch in kiosk mode. Cover duplicate callbacks and concurrent claims.

## 4 — Printer preparation and release: IN PROGRESS

Prepared bundle storage/integrity checks are implemented and tested; see PREPARED-OUTPUT.md. A durable preparation worker, verified dispatch path and CUPS protocol adapter now exist with injected-renderer/transport tests. Real PDF/image rendering, Unicode invoice pagination, Linux startup wiring and completed-bundle cleanup are implemented; target-Pi execution and physical qualification remain pending. See PREPARATION-CUPS.md. Implement CUPS/IPP capabilities, routing/options, rendering, status mapping and invoice output. Stage prepared jobs without submitting them; release only after valid code claim. Test restart and unknown-outcome reconciliation. Test physical selected-page, duplex, colour and copy correctness.

## 5 — Touchscreen UI: IN PROGRESS

Large four-digit keypad, clear/backspace, accessible contrast, Preparing/Printing/Done/Assistance states, timed reset and no previous-customer data. Owner access requires separate authentication. Portal/dashboard retain familiar design and responsive behaviour.

## 6 — Qualification and independent release: PLANNED

Run docs/TEST-PLAN.md on real target Pi and printers. Produce install/backup/update/recovery instructions and a versioned Linux package. Verify Windows repository files and service configuration are untouched. Release only after payment-to-code-to-paper acceptance.
