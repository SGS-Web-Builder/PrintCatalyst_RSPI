-- Phase 6: Passport Photo Studio.
--
-- The studio manages the end-to-end workflow of taking a customer-uploaded
-- photo, optionally running a (deterministic, licence-reviewed) face-region
-- detector, letting the operator refine the bounding box, choosing a country
-- document preset, applying a background colour, laying out a printable
-- sheet of photos and rendering the final PNG.
--
-- The schema mirrors the master spec's three durable artefacts:
--
--   1. passport_sessions — one row per studio session. The session owns the
--      chosen preset, physical width / height in mm, background colour, sheet
--      preset, photos-per-sheet count, DPI, compliance note, status and the
--      reference to the source document (the existing documents table).
--
--   2. passport_face_regions — the head bounding box inside the source
--      document. Auto-detected rows are written with manual=0 and a
--      confidence value; operator-corrected rows are written with manual=1
--      and confidence=1.0 because the operator has accepted them as
--      authoritative. The unique index on session_id prevents a second
--      auto-detect from racing a manual save.
--
--   3. passport_outputs — durable record of every composed sheet, including
--      the storage path to the rendered PNG, the sheet's physical and pixel
--      sizes, the photo count and the source session. Re-composition writes
--      a new row rather than overwriting so the merchant can compare
--      alternative layouts.
--
-- All tables are additive: no existing table is altered. The session is
-- deletable by the merchant but the underlying document blob is retained per
-- the document retention policy; outputs are also retained so a re-printed
-- passport photo matches the original render.

CREATE TABLE passport_sessions (
    id                       TEXT    PRIMARY KEY NOT NULL,
    order_id                 TEXT             REFERENCES orders(id) ON DELETE SET NULL,
    document_id              TEXT    NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    preset                   TEXT    NOT NULL CHECK (preset IN (
                                'india_passport', 'india_visa', 'schengen_visa', 'uk_passport',
                                'us_passport', 'canada_passport', 'australia_passport', 'custom')),
    width_mm                 REAL    NOT NULL CHECK (width_mm > 0 AND width_mm <= 200),
    height_mm                REAL    NOT NULL CHECK (height_mm > 0 AND height_mm <= 200),
    background               TEXT    NOT NULL DEFAULT 'white'
                             CHECK (background IN ('keep', 'white', 'light_blue')),
    head_height_mm           REAL    NOT NULL CHECK (head_height_mm > 0 AND head_height_mm <= 200),
    eye_line_from_bottom_mm  REAL    NOT NULL CHECK (eye_line_from_bottom_mm > 0 AND eye_line_from_bottom_mm <= 200),
    sheet                    TEXT    NOT NULL DEFAULT 'A4'
                             CHECK (sheet IN ('A4', 'A3', 'Letter', '4x6')),
    photos_per_sheet         INTEGER NOT NULL DEFAULT 0 CHECK (photos_per_sheet >= 0 AND photos_per_sheet <= 64),
    dpi                      INTEGER NOT NULL DEFAULT 300 CHECK (dpi BETWEEN 72 AND 1200),
    compliance_note          TEXT    NOT NULL DEFAULT '',
    status                   TEXT    NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'auto_detected', 'manual_confirmed', 'composed', 'failed')),
    error                    TEXT    NOT NULL DEFAULT '',
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL,
    removed_at               INTEGER
);

CREATE INDEX passport_sessions_order_idx ON passport_sessions (order_id, created_at DESC);
CREATE INDEX passport_sessions_status_idx ON passport_sessions (status);

CREATE TABLE passport_face_regions (
    session_id   TEXT    PRIMARY KEY NOT NULL REFERENCES passport_sessions(id) ON DELETE CASCADE,
    x            INTEGER NOT NULL CHECK (x >= 0),
    y            INTEGER NOT NULL CHECK (y >= 0),
    width        INTEGER NOT NULL CHECK (width > 0),
    height       INTEGER NOT NULL CHECK (height > 0),
    confidence   REAL    NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 1),
    manual       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE passport_outputs (
    id              TEXT    PRIMARY KEY NOT NULL,
    session_id      TEXT    NOT NULL REFERENCES passport_sessions(id) ON DELETE CASCADE,
    side            TEXT    NOT NULL DEFAULT 'single' CHECK (side IN ('single')),
    sheet_width_mm  REAL    NOT NULL,
    sheet_height_mm REAL    NOT NULL,
    pixel_width     INTEGER NOT NULL CHECK (pixel_width > 0),
    pixel_height    INTEGER NOT NULL CHECK (pixel_height > 0),
    storage_path    TEXT    NOT NULL,
    sha256          TEXT    NOT NULL,
    photo_count     INTEGER NOT NULL DEFAULT 1 CHECK (photo_count >= 1 AND photo_count <= 64),
    created_at      INTEGER NOT NULL
);

CREATE INDEX passport_outputs_session_idx ON passport_outputs (session_id, created_at DESC);