# Instructions for every coding agent working here

## Scope and isolation

This Git repository is the Raspberry Pi kiosk product. Its parent directory is a separate Windows product repository. Work only inside this repository unless the human explicitly authorizes another target. Read-only inspection of the Windows reference is permitted. Never edit, commit, push or build the Windows application as part of kiosk work.

Before changes, check `git rev-parse --show-toplevel`, `git remote -v`, `git status --short`, and applicable instructions. The expected origin is https://github.com/SGS-Web-Builder/PrintCatalyst_RSPI.git. Do not copy the parent .git directory. Do not initialize a shared submodule or symlink to mutable Windows sources.

Read docs/HANDOFF.md first. It records implementation status, not claims that planned features already exist. Inspect actual code before extending it. Preserve other chats' unfinished changes. Do not have two chats edit the same files concurrently; use separate branches/worktrees when needed and record ownership in the handoff.

## Product invariants

- Retain portal/dashboard design and functional parity, public Cloudflare portal, Razorpay verification and read-only mobile merchant monitoring.
- By default, a verified paid order waits for a four-digit code entered on the trusted physical kiosk. The owner may explicitly disable OTP remotely (user decision 2026-10-30); then only server-verified, paid, prepared orders may auto-release. Preserve transactional policy/refund checks and duplicate prevention.
- User decision 2026-10-30: cash checkout is available when enabled, including without Razorpay. Only authenticated owner confirmation of full cash receipt authorizes preparation and the normal OTP/AutoPrint flow. Cash evidence must remain distinct from gateway verification.
- Browser redirects and client-supplied payment status cannot authorize payment or release.
- No duplicate automatic print after retry, double tap, timeout, browser reload or process restart.
- Ambiguous printer submission requires reconciliation/operator review, never blind resubmission.
- Preserve selected pages, copies, paper, colour, orientation-dependent duplex, image composition and invoice settings.
- Current user decision: default Linux/Pi packages are licence-free (licensefree build tag). No activation prompt or release-time licence requirement. Retain optional licensed-build code for future use. Keep pickup encryption and kiosk credentials protected.
- Kiosk local access is not merchant authorization. Do not expose merchant routes just because a touchscreen browser is loopback.
- No secrets, production customer data, publisher private keys or real payment credentials in source/tests/logs/screenshots.

## Completion and handoff

Run checks meaningful to the change. Never label an untested Linux printer driver or physical print path as working. Record exact commands, outcomes, files changed, outstanding risks and next steps in docs/HANDOFF.md. Keep implemented versus planned status explicit. Do not claim a commit/push/deployment happened without verifying it.

- User decision 2026-10-30: when owner selects all_documents, submitted unpaid cash orders may receive OTP immediately and print via OTP or explicitly disabled-OTP AutoPrint. Never label unpaid cash as paid or gateway-verified. Online orders still require verified capture. Turning policy off stops uncommitted unpaid claims; committed releases remain valid.
