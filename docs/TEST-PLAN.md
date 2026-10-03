# Acceptance and validation

## Automated

- Payment callback alone cannot mark paid; invalid signatures, wrong amount/currency/order are rejected.
- Duplicate/out-of-order payment events create one pickup code.
- Leading zeros survive display/input; collisions retry; exhausted namespace does not reuse codes.
- Unpaid, expired, used, wrong-kiosk and guessed codes cannot release jobs.
- Concurrent code entry/double tap produces one durable release request.
- Restart before claim, after claim, before submission and after uncertain printer acceptance does not blindly duplicate output.
- Preparation failure does not consume a code or lose a paid order.
- Public tunnel clients cannot call kiosk or owner mutation APIs; monitor sessions cannot release/modify anything.
- One-time activation survives offline restart with valid saved identity, rejects copied/tampered activation at startup, and performs no recurring network calls.
- Existing portal, pricing, selection, composition, invoice, retention and mobile-monitor regressions remain passing.

## Hardware matrix

For each actual Pi/RAM, OS/image version, touchscreen and printer/firmware, record USB/LAN path and driver package. Print PDFs, images, merged/collage layouts, selected/odd/even pages, A4/other supported sizes, monochrome/colour, portrait/landscape duplex and multiple copies. Check invoices on the configured tray. Test no paper, unplug/reconnect, spooler restart and power loss. Never equate CUPS acceptance with finished paper.

## Performance

Record timestamps at server-verified payment, code published, preparation complete, final keypad entry, release committed, CUPS accepted, physical first page and completion. Measure cold/warm runs and small/large documents with concurrent uploads. Report p50/p95/max and sample count. Include slow/offline external services after activation. Define an agreed hardware-specific acceptance budget from measurements; no zero-delay promise.

## Release gate

No customer data/secrets in artifacts. No Windows service changes. Fresh install/reboot works; customer browser fullscreen starts automatically; owner recovery works; paid order and code survive restart; mobile monitoring shows correct status. Real test-mode payment and physical output must be demonstrated before production sign-off.
