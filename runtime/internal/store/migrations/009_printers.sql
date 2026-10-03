-- Phase 4: advanced printer framework.
--
-- The capability model in the master spec requires three things at rest:
--   1. a stable Printer identity (OS queue or IPP endpoint) plus its raw
--      identifiers and the discovery fingerprint;
--   2. a normalized capability snapshot per printer, kept verbatim alongside
--      the raw attributes the printer reported;
--   3. a merchant-controlled enable/disable flag per printer plus a per-
--      capability verification row that captures the
--      advertised → enabled → tested → confirmed → verified lifecycle.
--
-- Capability rows are keyed by normalized identifiers (paper size, tray,
-- finishing position, etc.) so a driver / endpoint upgrade can re-bind to
-- the same row by normalized key and surface only the change to the
-- merchant. Removed capabilities are not deleted; they are marked
-- `removed_at` so historical orders retain a pointer to the configuration
-- they were printed with.

CREATE TABLE printers (
    id              TEXT    PRIMARY KEY NOT NULL,
    backend         TEXT    NOT NULL CHECK (backend IN ('ipp', 'ipps', 'windows', 'cups', 'mock')),
    queue_name      TEXT    NOT NULL,
    display_name    TEXT    NOT NULL DEFAULT '',
    driver_name     TEXT    NOT NULL DEFAULT '',
    driver_version  TEXT    NOT NULL DEFAULT '',
    uri             TEXT    NOT NULL DEFAULT '',
    location        TEXT    NOT NULL DEFAULT '',
    fingerprint     TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'unknown'
                    CHECK (status IN ('unknown', 'ready', 'offline', 'error')),
    enabled         INTEGER NOT NULL DEFAULT 0,
    is_default      INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    last_seen_at    INTEGER NOT NULL,
    removed_at      INTEGER
);

CREATE INDEX printers_backend_idx ON printers (backend);
CREATE INDEX printers_enabled_idx ON printers (enabled);
CREATE UNIQUE INDEX printers_queue_backend_unique
    ON printers (backend, queue_name) WHERE removed_at IS NULL;

CREATE TABLE printer_capabilities (
    id              TEXT    PRIMARY KEY NOT NULL,
    printer_id      TEXT    NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    fingerprint     TEXT    NOT NULL,
    captured_at     INTEGER NOT NULL,
    raw_attributes  TEXT    NOT NULL, -- JSON: the original attributes the backend reported
    normalized_json TEXT    NOT NULL  -- JSON: the normalized capability snapshot
);

CREATE INDEX printer_capabilities_printer_idx
    ON printer_capabilities (printer_id, captured_at DESC);

CREATE TABLE printer_paper_sizes (
    id              TEXT    PRIMARY KEY NOT NULL,
    printer_id      TEXT    NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    paper_key       TEXT    NOT NULL,
    raw_label       TEXT    NOT NULL,
    width_mm        INTEGER NOT NULL DEFAULT 0,
    height_mm       INTEGER NOT NULL DEFAULT 0,
    is_custom       INTEGER NOT NULL DEFAULT 0,
    min_width_mm    INTEGER NOT NULL DEFAULT 0,
    max_width_mm    INTEGER NOT NULL DEFAULT 0,
    min_height_mm   INTEGER NOT NULL DEFAULT 0,
    max_height_mm   INTEGER NOT NULL DEFAULT 0,
    removed_at      INTEGER
);

CREATE UNIQUE INDEX printer_paper_sizes_printer_key
    ON printer_paper_sizes (printer_id, paper_key) WHERE removed_at IS NULL;

CREATE TABLE printer_finishing_options (
    id              TEXT    PRIMARY KEY NOT NULL,
    printer_id      TEXT    NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    finishing_type  TEXT    NOT NULL
                    CHECK (finishing_type IN ('duplex', 'staple', 'punch', 'fold', 'booklet', 'output_bin')),
    raw_label       TEXT    NOT NULL,
    normalized_key  TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 0,
    removed_at      INTEGER
);

CREATE INDEX printer_finishing_options_printer_idx
    ON printer_finishing_options (printer_id, finishing_type);

CREATE TABLE printer_verifications (
    id              TEXT    PRIMARY KEY NOT NULL,
    printer_id      TEXT    NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    capability_type TEXT    NOT NULL
                    CHECK (capability_type IN ('paper_size', 'colour_mode', 'duplex', 'tray', 'finishing', 'general')),
    capability_key  TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'tested', 'confirmed', 'verified', 'failed')),
    evidence        TEXT    NOT NULL DEFAULT '',
    tested_at       INTEGER NOT NULL DEFAULT 0,
    verified_at     INTEGER NOT NULL DEFAULT 0,
    verified_by     TEXT    NOT NULL DEFAULT '',
    invalidated_at  INTEGER
);

CREATE INDEX printer_verifications_printer_idx
    ON printer_verifications (printer_id, capability_type, capability_key);
