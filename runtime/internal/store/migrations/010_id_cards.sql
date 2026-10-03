-- Phase 5: ID Card Studio.
--
-- The studio manages the end-to-end workflow of taking customer-uploaded
-- images (or pages extracted from a PDF), correcting EXIF orientation,
-- detecting (or letting the operator refine) the card quadrilateral,
-- composing the front and back onto a chosen sheet at physical dimensions
-- the printer can hit, and recording merchant calibration offsets that
-- align duplex print jobs.
--
-- The schema mirrors the master spec's three durable artefacts:
--
--   1. id_card_calibrations — per (sheet, flip-edge) printer alignment.
--      The merchant captures one row after running a duplex test page
--      and measures where the back card lands relative to the front. The
--      dx/dy offsets (in millimetres) are then applied at layout time so
--      the studio never silently drifts on a particular printer.
--
--   2. id_card_sessions — one row per studio session. The session owns
--      the chosen sheet, card preset, layout kind, flip edge, custom
--      dimensions when applicable, calibration pointer, status, and the
--      produced output. Each session references one or two customer
--      documents (front, optional back) via the existing documents table.
--
--   3. id_card_corners — per-document corner set. Auto-detected corners
--      are written with manual=0 and a confidence value; operator-corrected
--      corners are written with manual=1 and confidence=1.0 because the
--      operator has accepted them as authoritative.
--
--   4. id_card_outputs — durable record of every composed sheet, including
--      the storage path to the rendered PNG, the sheet's physical and pixel
--      sizes, and the source session. A session may have multiple outputs
--      (e.g., a front and a duplex back) and historical outputs are
--      retained even after a re-composition so an audit can verify the
--      exact pixels the merchant printed.
--
-- All tables are additive: no existing table is altered. The session is
-- deletable by the merchant but the underlying documents blob rows are
-- retained per the document retention policy; outputs are also retained so
-- a re-printed card matches the original print.

CREATE TABLE id_card_calibrations (
    id              TEXT    PRIMARY KEY NOT NULL,
    name            TEXT    NOT NULL,
    sheet           TEXT    NOT NULL CHECK (sheet IN ('A4', 'A3', 'Letter', 'Legal')),
    flip_edge       TEXT    NOT NULL CHECK (flip_edge IN ('long_edge', 'short_edge')),
    dx_mm           REAL    NOT NULL DEFAULT 0 CHECK (dx_mm BETWEEN -25 AND 25),
    dy_mm           REAL    NOT NULL DEFAULT 0 CHECK (dy_mm BETWEEN -25 AND 25),
    notes           TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX id_card_calibrations_sheet_idx ON id_card_calibrations (sheet);

CREATE TABLE id_card_sessions (
    id              TEXT    PRIMARY KEY NOT NULL,
    order_id        TEXT    REFERENCES orders(id) ON DELETE SET NULL,
    front_doc_id    TEXT    NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    back_doc_id     TEXT             REFERENCES documents(id) ON DELETE RESTRICT,
    sheet           TEXT    NOT NULL CHECK (sheet IN ('A4', 'A3', 'Letter', 'Legal')),
    card            TEXT    NOT NULL CHECK (card IN ('cr80', 'aadhaar', 'pan', 'voter', 'driving', 'visiting', 'custom')),
    card_width_mm   REAL    NOT NULL CHECK (card_width_mm > 0 AND card_width_mm <= 400),
    card_height_mm  REAL    NOT NULL CHECK (card_height_mm > 0 AND card_height_mm <= 400),
    layout_kind     TEXT    NOT NULL CHECK (layout_kind IN ('front_only', 'side_by_side', 'vertical', 'duplex_front', 'duplex_back')),
    flip_edge       TEXT    NOT NULL DEFAULT 'long_edge' CHECK (flip_edge IN ('long_edge', 'short_edge')),
    rows            INTEGER NOT NULL DEFAULT 1 CHECK (rows BETWEEN 1 AND 8),
    cols            INTEGER NOT NULL DEFAULT 1 CHECK (cols BETWEEN 1 AND 8),
    actual_size     INTEGER NOT NULL DEFAULT 1,
    dpi             INTEGER NOT NULL DEFAULT 300 CHECK (dpi BETWEEN 72 AND 1200),
    calibration_id  TEXT             REFERENCES id_card_calibrations(id) ON DELETE SET NULL,
    status          TEXT    NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'auto_detected', 'manual_confirmed', 'composed', 'failed')),
    error           TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    removed_at      INTEGER
);

CREATE INDEX id_card_sessions_order_idx ON id_card_sessions (order_id, created_at DESC);
CREATE INDEX id_card_sessions_status_idx ON id_card_sessions (status);

CREATE TABLE id_card_corners (
    id              TEXT    PRIMARY KEY NOT NULL,
    session_id      TEXT    NOT NULL REFERENCES id_card_sessions(id) ON DELETE CASCADE,
    document_id     TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    side            TEXT    NOT NULL CHECK (side IN ('front', 'back')),
    tl_x            REAL    NOT NULL,
    tl_y            REAL    NOT NULL,
    tr_x            REAL    NOT NULL,
    tr_y            REAL    NOT NULL,
    br_x            REAL    NOT NULL,
    br_y            REAL    NOT NULL,
    bl_x            REAL    NOT NULL,
    bl_y            REAL    NOT NULL,
    confidence      REAL    NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 1),
    manual          INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX id_card_corners_session_side_unique
    ON id_card_corners (session_id, side);

CREATE TABLE id_card_outputs (
    id              TEXT    PRIMARY KEY NOT NULL,
    session_id      TEXT    NOT NULL REFERENCES id_card_sessions(id) ON DELETE CASCADE,
    side            TEXT    NOT NULL CHECK (side IN ('front', 'back')),
    sheet_width_mm  REAL    NOT NULL,
    sheet_height_mm REAL    NOT NULL,
    pixel_width     INTEGER NOT NULL CHECK (pixel_width > 0),
    pixel_height    INTEGER NOT NULL CHECK (pixel_height > 0),
    storage_path    TEXT    NOT NULL,
    sha256          TEXT    NOT NULL,
    created_at      INTEGER NOT NULL
);

CREATE INDEX id_card_outputs_session_idx ON id_card_outputs (session_id, side, created_at DESC);
