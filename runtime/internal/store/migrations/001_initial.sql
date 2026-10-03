CREATE TABLE provisioning_gates (
    gate TEXT PRIMARY KEY NOT NULL,
    completed_at TEXT,
    evidence TEXT NOT NULL DEFAULT ''
);

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY NOT NULL,
    event_type TEXT NOT NULL,
    evidence TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);

CREATE TABLE print_jobs (
    id TEXT PRIMARY KEY NOT NULL,
    local_order_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    total_minor INTEGER NOT NULL CHECK (total_minor >= 0),
    currency TEXT NOT NULL CHECK (length(currency) = 3),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE outbox_events (
    id TEXT PRIMARY KEY NOT NULL,
    aggregate_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (aggregate_id, event_type)
);

CREATE INDEX outbox_events_delivery_idx
ON outbox_events (status, available_at, created_at);
