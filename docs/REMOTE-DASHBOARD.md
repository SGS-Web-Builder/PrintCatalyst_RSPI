# Remote kiosk management

Open `/remote/` on the kiosk's configured and verified HTTPS domain. Use the owner account. `/monitor/` remains read-only and cannot authorize management writes. Cloudflare is not provisioned by this code.

## Implemented

- Separate owner-only sessions, secure scoped cookies, same-origin and CSRF checks, expiry/logout. Remote advertisements use the same session at `/remote/ads/`; the older separate `/ads-admin/` surface remains supported.
- Live order list/search/date/status filters, attention counts, estimated paper stock and document-volume free space. Printing remains visible when print_requested is set. Queue completion is not proof of physical delivery.
- Maintenance closes new intake, including a transactional checkout recheck. Already-paid physical OTP releases continue. Existing documents and submitted orders are retained.
- Shared/per-printer pricing, quantity tiers, free-price confirmation and revision checks against concurrent saves or business currency changes. Scheduled changes persist across restart. Intervening price changes produce review status; an interrupted application is not silently replayed. Existing order totals are not rewritten. The worker checks schedules every 15 seconds plus local polling time, not at a hard real-time deadline.
- Printer health through read-only local CUPS Get-Printer-Attributes: state/reasons/message, model/driver, accepting-jobs, queue count and available marker/supply levels. Missing, mismatched and negative sentinel readings remain unavailable. This is scheduler-reported data and can be cached by the driver; it is not a guarantee of fresh physical sensor readings. No invented exact paper counts or per-order physical output counters.
- Gross recorded collections grouped by currency/precision; refund records separate. Razorpay settlements are fetched in pages of 25 and are explicitly account-wide, not kiosk-specific bank deposits or settlement forecasts.
- Full online refunds for verified captured orders before physical release. A durable immutable request ID is written before calling Razorpay; lost responses require reconciliation with that same ID. After a gateway refund ID is known, retries fetch status rather than create again. Any refund record blocks OTP claim. Pending, processed and review states are shown to customers. Released/partial-print refunds remain an operator workflow in Razorpay after inspecting output. Financial history is retained; processed refunds do not delete orders or documents.
- Recovery actions: retry failed preparation, replace an expired unused code, and explicitly confirm all physical output. Confirmation uses the existing service and permits later retention cleanup; it never prints or blindly resubmits jobs.
- Guarded Linux service restart, with durable command status. Requires maintenance, no unresolved submitted orders, and known idle enabled queues. It signals only this application, not CUPS/the OS/printer. The installed systemd unit uses Restart=always; direct process launches cannot use the control. A later startup acknowledges restarting commands. Actual Linux lifecycle acceptance remains required.
- Signed software archive staging (maximum 80 MiB). Configure `PC_UPDATE_PUBLIC_KEY_FILE` locally with the base64 Ed25519 publisher public key. Signature is base64 Ed25519 over UTF-8 `PrintCatalyst-Pi-update-v1\n<lowercase SHA256 hex>\n`. Private signing keys never belong on kiosks. Archives are stored under the protected data directory's `updates/<sha256>.tar.gz`. The dashboard does NOT execute installers; local installation during maintenance is still required. This avoids giving the web process root software-installation privileges. Automatic rollout/rollback is not implemented.
- Management audit records for maintenance, pricing revisions/schedule state, refund state and service command state.
- Separate deployable fleet heartbeat service, persistent last-seen state and up to 50 significant events per kiosk. See `packaging/fleet/README.md`. Per-device tokens cannot read other kiosks; the fleet dashboard uses a separate admin key. No customer documents/OTP/payment credentials leave the Pi. Central command relay, fleet-wide price/ad push, email/SMS alerts and aggregated financial reporting are not implemented.

## Epson reference and integration boundary

The EM-C800 documentation lists embedded ERS support. ERS authentication, API schema/permissions and your central deployment credentials are pending; the user confirmed they will obtain them. No ERS endpoint or capability has been invented, and no printer firmware, reboot or printhead-cleaning command is exposed through a guessed API. Use the supplied Epson tools for those operations until supported integration details are available.

References consulted:
- Epson EM-C800 guide: https://download4.epson.biz/sec_pubs/em-c800_series/useg/en/manual.pdf
- Epson EM-C800 specifications: https://download.epson.com.sg/product_brochures/EM-C800/WF_EMC800.pdf
- ERS features: https://epson.com/Accessories/Printer-Accessories/Epson-Remote-Services/p/ERS000001
- EM-C800 driver/software list: https://support.epson.net/terms/ijp/?LG2=EN&MKN=EM-C800&PINF=swlist
- CUPS marker attributes and unavailable sentinel values: https://www.cups.org/doc/spec-ipp.html
- Razorpay refund idempotency: https://github.com/razorpay/markdown-docs/blob/master/api/refunds/normal-refunds-idempotent.md
- Razorpay settlements: https://github.com/razorpay/razorpay-node/blob/master/documents/settlement.md

## Acceptance still required

Install on a test Pi, configure HTTPS, exercise owner/session/logout and pricing changes from a phone, verify paused intake with an existing paid OTP, and test Epson queue/supply readings against the printer panel. Exercise refunds/settlements in Razorpay Test Mode before any live use. Verify systemd restart with empty queues, paid-order retention and command acknowledgement. Deploy the central service separately and unplug a test Pi to confirm stale/offline status. Customer documents, live payments, ERS accounts and real printer operations were not used during development.

## OTP policy and live touchscreen (2026-10-30)

Remote dashboard > Kiosk operations > Print release and screen now has independent OTP and success-countdown controls. Both default to enabled. Disabling OTP explicitly authorizes AutoPrint for eligible already-waiting and future verified paid orders when their prepared bundle is ready; no customer code is displayed. Internal pickup/release journals remain for duplicate protection. Refund holds, payment identity/amount/currency, preparation, prior submission history and policy are checked transactionally. Re-enabling OTP stops automatic claims not yet committed; already-authorized releases continue. The trusted screen shows automatic orders sequentially. Submitted/refunded/uncertain jobs are never blindly retried.

The private screen uses authenticated POST event streams (250 ms database-state checks, unchanged-state heartbeat, bounded stream/reconnect) and retains receipt-based polling fallback. OTP submission displays a circular activity indicator immediately. It then shows preparation/printing, green success only on confirmed done, or assistance for uncertain/error states. No percentage or completion is fabricated. Existing CUPS completion-review semantics remain: until hardware evidence or merchant confirmation establishes delivery, scheduler completion alone does not produce green success or delete source files.

Success countdown enabled: five seconds then reset. Disabled: no visible countdown, success remains for 1.5 seconds then reset. There is no countdown before printing. Security/rate-limit cooldowns are independent and cannot be disabled by this setting. Returning to idle shows OTP entry when OTP is enabled, otherwise the automatic-printing waiting screen. The success setting is applied when the success screen starts.

The private screen endpoint remains loopback-only and paired/authenticated; no progress receipt or customer identity is exposed on a public event URL. Actual device reporting latency is not zero and has not been measured on Epson hardware. The configured CUPS monitoring cycle is 500 ms; event delivery follows the next local status check.
