-- Initial schema — docs/ARCHITECTURE.md §4.
-- Money is integer cents. Timestamps are TIMESTAMPTZ (stored UTC, shown in Africa/Nairobi).

CREATE EXTENSION IF NOT EXISTS pgcrypto; -- gen_random_uuid()

CREATE TABLE users (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    phone_e164  TEXT NOT NULL UNIQUE,
    name        TEXT,
    email       TEXT,
    role        TEXT NOT NULL DEFAULT 'FAN'
                CHECK (role IN ('FAN', 'ORGANISER', 'ADMIN', 'GATE_STAFF')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Physical layout: permanent per venue.
-- ---------------------------------------------------------------------------

CREATE TABLE venues (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         TEXT NOT NULL,
    city         TEXT NOT NULL,
    capacity     INTEGER NOT NULL CHECK (capacity >= 0),
    -- The coordinate space the stadium plan is drawn in.
    plan_width   INTEGER NOT NULL,
    plan_height  INTEGER NOT NULL
);

CREATE TABLE stands (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue_id       BIGINT NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    code           TEXT NOT NULL,
    name           TEXT NOT NULL,
    tier           TEXT NOT NULL CHECK (tier IN ('LOWER', 'UPPER')),
    polygon        JSONB NOT NULL DEFAULT '[]', -- outline on the stadium overview map
    display_order  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (venue_id, code, tier)
);

CREATE TABLE blocks (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stand_id      BIGINT NOT NULL REFERENCES stands (id) ON DELETE CASCADE,
    code          TEXT NOT NULL,
    label         TEXT NOT NULL,
    origin_x      DOUBLE PRECISION NOT NULL,
    origin_y      DOUBLE PRECISION NOT NULL,
    rotation_deg  DOUBLE PRECISION NOT NULL DEFAULT 0,
    arc_radius    DOUBLE PRECISION, -- NULL = straight rectangular block
    seat_pitch_x  DOUBLE PRECISION NOT NULL,
    seat_pitch_y  DOUBLE PRECISION NOT NULL,
    UNIQUE (stand_id, code)
);

CREATE TABLE rows (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    block_id    BIGINT NOT NULL REFERENCES blocks (id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    row_index   INTEGER NOT NULL,
    seat_count  INTEGER NOT NULL CHECK (seat_count >= 0),
    x_offset    DOUBLE PRECISION NOT NULL DEFAULT 0,
    UNIQUE (block_id, row_index)
);

CREATE TABLE seats (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    row_id       BIGINT NOT NULL REFERENCES rows (id) ON DELETE CASCADE,
    block_id     BIGINT NOT NULL REFERENCES blocks (id) ON DELETE CASCADE,
    seat_number  INTEGER NOT NULL,
    label        TEXT NOT NULL,
    -- Pre-computed at layout-save time so the app never does trigonometry at render time.
    x            DOUBLE PRECISION NOT NULL,
    y            DOUBLE PRECISION NOT NULL,
    seat_type    TEXT NOT NULL DEFAULT 'STANDARD'
                 CHECK (seat_type IN ('STANDARD', 'ACCESSIBLE', 'COMPANION', 'RESTRICTED_VIEW')),
    is_active    BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (row_id, seat_number)
);
CREATE INDEX seats_block_id_idx ON seats (block_id);

-- ---------------------------------------------------------------------------
-- Events, pricing and per-event inventory.
-- ---------------------------------------------------------------------------

CREATE TABLE events (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue_id              BIGINT NOT NULL REFERENCES venues (id),
    title                 TEXT NOT NULL,
    competition           TEXT,
    home_team             TEXT,
    away_team             TEXT,
    kickoff_at            TIMESTAMPTZ NOT NULL,
    gates_open_at         TIMESTAMPTZ,
    poster_url            TEXT,
    status                TEXT NOT NULL DEFAULT 'DRAFT'
                          CHECK (status IN ('DRAFT', 'PUBLISHED', 'ON_SALE', 'SOLD_OUT', 'CANCELLED', 'COMPLETED')),
    sales_open_at         TIMESTAMPTZ,
    sales_close_at        TIMESTAMPTZ,
    max_tickets_per_user  INTEGER NOT NULL DEFAULT 6 CHECK (max_tickets_per_user > 0),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX events_kickoff_at_idx ON events (kickoff_at);

CREATE TABLE event_categories (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id     BIGINT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    price_cents  BIGINT NOT NULL CHECK (price_cents >= 0),
    currency     TEXT NOT NULL DEFAULT 'KES',
    color_hex    TEXT NOT NULL,
    description  TEXT,
    UNIQUE (event_id, name)
);

CREATE TABLE holds (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id    BIGINT NOT NULL REFERENCES events (id),
    user_id     BIGINT NOT NULL REFERENCES users (id),
    expires_at  TIMESTAMPTZ NOT NULL,
    status      TEXT NOT NULL DEFAULT 'ACTIVE'
                CHECK (status IN ('ACTIVE', 'CONVERTED', 'EXPIRED', 'RELEASED')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX holds_user_event_idx ON holds (user_id, event_id);

-- One row per seat per event, materialised when the organiser publishes the event.
CREATE TABLE event_seats (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id         BIGINT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    seat_id          BIGINT NOT NULL REFERENCES seats (id),
    -- Copied from seats so the level-3 seat map query needs no join to filter by block.
    block_id         BIGINT NOT NULL REFERENCES blocks (id),
    category_id      BIGINT REFERENCES event_categories (id),
    status           TEXT NOT NULL DEFAULT 'AVAILABLE'
                     CHECK (status IN ('AVAILABLE', 'HELD', 'SOLD', 'BLOCKED')),
    hold_id          BIGINT REFERENCES holds (id),
    hold_expires_at  TIMESTAMPTZ,
    version          INTEGER NOT NULL DEFAULT 0,
    UNIQUE (event_id, seat_id)
);
CREATE INDEX event_seats_event_block_status_idx ON event_seats (event_id, block_id, status);
-- Lets the expiry sweeper find lapsed holds without scanning the whole table.
CREATE INDEX event_seats_hold_expiry_idx ON event_seats (hold_expires_at) WHERE status = 'HELD';

-- ---------------------------------------------------------------------------
-- Bookings, payments, tickets.
-- ---------------------------------------------------------------------------

CREATE TABLE bookings (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    reference        TEXT NOT NULL UNIQUE, -- human-readable, e.g. TLT-8H3K2M
    event_id         BIGINT NOT NULL REFERENCES events (id),
    user_id          BIGINT NOT NULL REFERENCES users (id),
    hold_id          BIGINT REFERENCES holds (id),
    status           TEXT NOT NULL DEFAULT 'PENDING_PAYMENT'
                     CHECK (status IN ('PENDING_PAYMENT', 'PAID', 'FAILED', 'EXPIRED', 'CANCELLED',
                                       'REFUND_REQUIRED', 'REFUNDED')),
    subtotal_cents   BIGINT NOT NULL CHECK (subtotal_cents >= 0),
    fee_cents        BIGINT NOT NULL DEFAULT 0 CHECK (fee_cents >= 0),
    total_cents      BIGINT NOT NULL CHECK (total_cents >= 0),
    idempotency_key  TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at          TIMESTAMPTZ,
    -- A double-tapped Pay button must not create two bookings.
    UNIQUE (user_id, idempotency_key)
);

CREATE TABLE booking_items (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    booking_id        BIGINT NOT NULL REFERENCES bookings (id) ON DELETE CASCADE,
    event_seat_id     BIGINT NOT NULL REFERENCES event_seats (id),
    category_id       BIGINT NOT NULL REFERENCES event_categories (id),
    -- Snapshot: later price changes must not move past bookings.
    unit_price_cents  BIGINT NOT NULL CHECK (unit_price_cents >= 0),
    UNIQUE (booking_id, event_seat_id)
);

CREATE TABLE payments (
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    booking_id           BIGINT NOT NULL REFERENCES bookings (id),
    provider             TEXT NOT NULL DEFAULT 'MPESA' CHECK (provider IN ('MPESA')),
    amount_cents         BIGINT NOT NULL CHECK (amount_cents >= 0),
    phone_e164           TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'INITIATED'
                         CHECK (status IN ('INITIATED', 'PENDING', 'SUCCESS', 'FAILED', 'TIMEOUT', 'REVERSED')),
    merchant_request_id  TEXT,
    -- The callback handler is idempotent on this value.
    checkout_request_id  TEXT UNIQUE,
    mpesa_receipt        TEXT,
    result_code          INTEGER,
    result_desc          TEXT,
    raw_callback         JSONB,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at         TIMESTAMPTZ
);
CREATE INDEX payments_booking_id_idx ON payments (booking_id);
-- Lets the reconciliation worker find payments still waiting on a callback.
CREATE INDEX payments_pending_idx ON payments (created_at) WHERE status = 'PENDING';

CREATE TABLE tickets (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id     BIGINT NOT NULL REFERENCES bookings (id),
    event_seat_id  BIGINT NOT NULL REFERENCES event_seats (id),
    holder_name    TEXT,
    holder_phone   TEXT,
    token_nonce    BYTEA NOT NULL,
    status         TEXT NOT NULL DEFAULT 'VALID'
                   CHECK (status IN ('VALID', 'USED', 'VOID', 'TRANSFERRED')),
    issued_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    used_at        TIMESTAMPTZ
);
CREATE INDEX tickets_booking_id_idx ON tickets (booking_id);
-- A seat can only ever carry one live ticket; a transferred or voided one frees it for reissue.
CREATE UNIQUE INDEX tickets_live_seat_idx ON tickets (event_seat_id) WHERE status IN ('VALID', 'USED');

CREATE TABLE scan_logs (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- No foreign key: an offline gate can scan a forged or unknown ticket, and that
    -- attempt still has to be recorded.
    ticket_id   UUID NOT NULL,
    gate_id     TEXT NOT NULL,
    scanned_by  BIGINT REFERENCES users (id),
    scanned_at  TIMESTAMPTZ NOT NULL,
    device_id   TEXT NOT NULL,
    result      TEXT NOT NULL CHECK (result IN ('ADMITTED', 'DUPLICATE', 'INVALID', 'WRONG_EVENT')),
    synced_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Offline devices re-upload their batch after a failed sync; this makes that a no-op.
    UNIQUE (ticket_id, device_id, scanned_at)
);
