-- 021_order_print_options.sql — extend the schema with the customer-facing
-- print options that the portal exposes:
--
--   orientation       — auto / portrait / landscape. Auto means the
--                       backend picks the orientation per page based on
--                       width×height; the other two force it.
--   pages_per_sheet   — 1 / 2 / 4. When >1, the OS print driver is
--                       asked to compose multiple document pages onto a
--                       single output sheet (n-up). We never compose
--                       pages in our PDF — the driver handles it.
--   primary_printer_id — when set, every line of the order is
--                       dispatched to that printer. When empty, the
--                       dispatcher uses the merchant's primary printer
--                       or the first enabled printer.
--
-- The documents table also gets the same orientation hint stored so the
-- dispatcher can pre-rotate the bytes it sends to the spooler when the
-- driver does not honour dmOrientation directly.
--
-- The documents MIME check is widened to allow Word documents (.docx).
-- SQLite does not allow expanding a CHECK constraint in place, so we
-- rebuild the table inside a transaction. The rebuild is safe because
-- documents are immutable customer blobs — every column we copy is
-- stable across this migration.
ALTER TABLE order_lines ADD COLUMN orientation TEXT NOT NULL DEFAULT 'auto'
  CHECK (orientation IN ('auto','portrait','landscape'));
ALTER TABLE order_lines ADD COLUMN pages_per_sheet INTEGER NOT NULL DEFAULT 1
  CHECK (pages_per_sheet IN (1, 2, 4));
ALTER TABLE orders ADD COLUMN primary_printer_id TEXT NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN orientation_hint TEXT NOT NULL DEFAULT 'auto'
  CHECK (orientation_hint IN ('auto','portrait','landscape'));

-- Widen the MIME type check to include Word documents. The old table is
-- rebuilt with the new constraint, every row copied verbatim, the old
-- table dropped, and the new one renamed in place.
CREATE TABLE documents_v2 (
    id               TEXT    PRIMARY KEY NOT NULL,
    order_id         TEXT    REFERENCES orders(id) ON DELETE CASCADE,
    original_filename TEXT   NOT NULL,
    mime_type        TEXT    NOT NULL CHECK (mime_type IN (
                           'application/pdf',
                           'image/jpeg',
                           'image/png',
                           'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
                       )),
    size_bytes       INTEGER NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 52428800),
    page_count       INTEGER NOT NULL CHECK (page_count >= 1),
    sha256           TEXT    NOT NULL,
    storage_path     TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    retention_until  INTEGER NOT NULL,
    dispatched_at    INTEGER NOT NULL DEFAULT 0,
    orientation_hint TEXT    NOT NULL DEFAULT 'auto'
);

INSERT INTO documents_v2 (
    id, order_id, original_filename, mime_type, size_bytes, page_count,
    sha256, storage_path, created_at, retention_until, dispatched_at,
    orientation_hint
)
SELECT id, order_id, original_filename, mime_type, size_bytes, page_count,
       sha256, storage_path, created_at, retention_until, dispatched_at,
       'auto'
FROM documents;

DROP TABLE documents;
ALTER TABLE documents_v2 RENAME TO documents;

CREATE INDEX documents_order_id_idx ON documents (order_id);

-- Stored composite document the customer built by enabling the
-- "merge uploads into one PDF" toggle. When a row exists for an order
-- the dispatcher fetches the merged bytes instead of the per-line
-- document blobs. The merged blob is produced by the portal's render
-- step (see documents.Merge) and stored under dataDir/documents-merged/.
CREATE TABLE IF NOT EXISTS merged_documents (
    order_id        TEXT PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    storage_path    TEXT NOT NULL,
    mime_type       TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL CHECK (size_bytes >= 0),
    page_count      INTEGER NOT NULL CHECK (page_count >= 1),
    sha256          TEXT NOT NULL,
    created_at      INTEGER NOT NULL
);
