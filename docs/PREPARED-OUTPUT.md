# Prepared print bundle storage

Implemented development component: `runtime/internal/prepared`. This package accepts already-rendered PDF bytes; it does not convert documents, validate complete PDF syntax, contact a printer, or mark pickup records ready. It is not yet wired into runtime preparation/dispatch.

## Frozen output contract

A bundle contains an ordered manifest and numbered PDFs. The manifest binds the order ID, each line ID, queue, document/invoice role, paper, tray, colour, sides, copies, file size and PDF SHA-256. Its canonical JSON SHA-256 is the bundle identity. Selected source pages, rotation/orientation and pages-per-sheet composition must already be rendered into the supplied PDF. Do not send original page ranges or N-up options to CUPS again. The renderer must translate orientation-dependent duplex into the correct final long/short-edge option.

Invoices can follow documents in the same ordered bundle with their own paper and tray. Queue-threshold invoice decisions must be frozen by the release-group design before final publication; this storage component does not decide that policy. This dependency still needs to be resolved in the preparation/release integration.

`Publish` validates limits/settings, writes a private random staging directory, syncs files and (on Linux) directories, then atomically renames the complete bundle to its digest. Repeated and concurrent identical preparation verifies/reuses the existing result. Corrupt existing bundles are rejected, never silently replaced. Incomplete staging directories are not addressable by a valid digest. Crash-left staging cleanup remains future work; no broad deletion is performed here.

`Load(orderID, digest)` checks the manifest digest, exact order identity, schema, settings and every PDF size/hash before returning anything. A missing invoice or modified document fails the whole load. The dispatcher must submit the returned byte slices; reopening upload paths after verification would discard this guarantee. The caller must not mutate input buffers concurrently with Publish.

The store uses `os.Root` to confine filesystem operations. On Linux its existing root directory must be service-owned with no group/other permissions; new directories/files use 0700/0600. Symlink bundle roots/files are rejected. Root or a compromised service account can still modify files or code: checksums provide integrity checks against the expected database digest, not protection from a privileged attacker. Prepared PDFs are private plaintext files under the service state directory; encrypted-disk deployment is separate.

Current limits: 100 jobs, 50 MiB per PDF, 512 MiB per order, 1 MiB manifest; copies 1–999. Load returns all verified bytes in memory, so memory budgeting/streaming must be reviewed on the target Pi before deployment. Header checking is not PDF validation; only the trusted renderer should publish validated output.

## Required integration, still pending

1. Freeze paid source documents/settings and resolve routing/capabilities. Render selected pages, images, N-up and invoices without changing customer settings.
2. Publish and verify the full bundle before `pickup.MarkPrepared`. Guard concurrent preparation/settings changes transactionally; recovery must not overwrite a ready or claimed order with a different digest.
3. Dispatch only after durable kiosk claim. Load the claimed digest, verify the order, and submit the returned frozen data/settings through CUPS with existing submission journals. Recheck configured printer availability without rerouting silently.
4. Implement CUPS progress/reconciliation; ambiguous submission requires review, never blind retry.
5. Integrate prepared-file retention after confirmed completion, orphan staging cleanup and restart recovery. Never delete paid uncollected output because its pickup code expired.

Windows-host tests cover idempotency, reopen/recovery, concurrent publishing, settings/queue binding, cross-order rejection, tampering, missing/truncated files, no partial-order return, invoice tray preservation and invalid inputs. Linux permission/symlink tests are cross-compiled, not executed on this host. ARM64 compilation is not physical printer qualification.
