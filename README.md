# Print Catalyst Raspberry Pi Kiosk

Independent Raspberry Pi touchscreen kiosk edition of Print Catalyst.

**Status: documentation foundation only. No application code has been imported, no ARM build exists, and no Pi/printer acceptance test has run.**

Repository: https://github.com/SGS-Web-Builder/PrintCatalyst_RSPI

## Start here — including a new chat or new account

Read `AGENTS.md`, then `docs/HANDOFF.md`, `docs/REQUIREMENTS.md`, `docs/ARCHITECTURE.md`, `docs/TECH-STACK.md`, and `docs/IMPLEMENTATION-PLAN.md` in that order. Update the handoff at the end of each work session.

The Windows application is the reference implementation, not a shared working directory. This repository must have its own source, dependencies, data, product identity and releases. Do not change Windows behaviour to implement kiosk behaviour.

## Customer journey

Scan QR → upload on phone → choose print settings → pay → verified payment produces a four-digit pickup code on the portal → enter code on the Pi touchscreen → prepared job goes to the printer → completion or assistance screen.

Payment alone must never release a kiosk job. No SMS is required.

## Documentation

- `docs/REQUIREMENTS.md`: approved scope and unresolved decisions.
- `docs/ARCHITECTURE.md`: boundaries, payment/release lifecycle and failure recovery.
- `docs/TECH-STACK.md`: inherited components and proposed Linux replacements.
- `docs/IMPLEMENTATION-PLAN.md`: staged work and exit criteria.
- `docs/TEST-PLAN.md`: functional, security, latency and hardware acceptance.
- `docs/HANDOFF.md`: current truth, next actions and copyable continuation prompt.
- `docs/DECISIONS.md`: decision log and proposals awaiting confirmation.

Do not distribute licence-server private keys, live databases, customer documents, payment secrets or tunnel tokens with this repository.
