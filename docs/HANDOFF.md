# Cross-chat handoff

Last updated: 2026-10-03 (Asia/Kolkata).

## Current status

Documentation foundation only. Repository was empty when cloned. Application code has NOT been copied, modified for Pi, compiled for ARM or tested on hardware. This is not a ready-to-install kiosk.

Local folder: C:/Users/sarth/Documents/PrintCatalyst-OnPremise/PrintCatalyst_RSPI
Origin: https://github.com/SGS-Web-Builder/PrintCatalyst_RSPI.git
Windows reference: C:/Users/sarth/Documents/PrintCatalyst-OnPremise

The Windows reference has active/uncommitted work from other chats. Never assume its HEAD contains the current files. Do not overwrite it or include its Git history/builds/credentials in a blind recursive copy.

## Latest user intent

Keep Windows product independent. Build a separate Pi touchscreen kiosk with the same portal, dashboard, mobile read-only merchant login, Cloudflare/domain/QR, Razorpay and other shop functionality. Customers pay on their phone and see a four-digit pickup code in the portal. Entering it on the kiosk releases the order quickly. No SMS. Recent licence decision: one-time activation, no recurring checks.

## Next task

Phase 1 of IMPLEMENTATION-PLAN.md: inspect the source reference, import a sanitized allowlist into THIS repository, record provenance, rename product/module identity and establish a passing baseline. Then implement Linux startup/printing prerequisites and the kiosk state machine. Do not start by changing the Windows code.

## Known technical blockers

No non-Windows print backend, PDF preview or licence key protection in the current Windows reference. Invoice rendering and LibreOffice discovery are Windows-specific. Linux USB/LAN driver compatibility and physical timings remain unknown. Do not claim that Go cross-compilation alone produces a working kiosk.

## For another account/computer

Clone this repository and read its documents; chat history and the Windows local folder are not automatically available. Obtain an authorized sanitized source snapshot of the Windows product for Phase 1. Do not request or upload live payment secrets, licence signing keys, customer files or databases. Use placeholders/test-mode fixtures.

## Copyable prompt

> Continue the PrintCatalyst_RSPI project. Read AGENTS.md and docs/HANDOFF.md first, then requirements, architecture, technology and implementation plan. Confirm this repository's Git root and origin. Work only in the Pi repository and preserve the Windows application. Current stage is documentation only; perform an audited source import before implementation. Preserve portal/dashboard/mobile-monitor/Cloudflare/payment parity. Payment creates a portal-only four-digit code; printing requires physical kiosk entry. Record tests, implemented versus planned status, and next steps in HANDOFF.md before finishing.

## Session log

- 2026-10-03: Cloned empty new repository into nested independent folder. Added requirements, architecture, technology/migration map, staged plan, test plan, decision log and agent instructions. No runtime changes or hardware tests.
