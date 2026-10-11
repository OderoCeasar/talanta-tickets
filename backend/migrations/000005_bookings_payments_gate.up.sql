-- Bookings, payments, refunds, tickets and scans: what the payment, ticketing and gate
-- milestones need beyond 000001.
--
-- The NOT NULL columns added to tickets and scan_logs have no default on purpose. Nothing has
-- ever written to those tables; if that is wrong this migration should stop, not invent values.

ALTER TABLE bookings
    -- Bearer secret in the SMS ticket link (§9.4). Generated here so no booking can lack one.
    ADD COLUMN web_token  TEXT NOT NULL DEFAULT encode(gen_random_bytes(32), 'hex'),
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD CONSTRAINT bookings_web_token_key UNIQUE (web_token),
    ADD CONSTRAINT bookings_total_check CHECK (total_cents = subtotal_cents + fee_cents);
CREATE TRIGGER bookings_set_updated_at BEFORE UPDATE ON bookings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- Correction 7: the sweeper expires the unpaid booking that belongs to a lapsed hold.
CREATE INDEX bookings_hold_id_idx ON bookings (hold_id) WHERE hold_id IS NOT NULL;
CREATE INDEX bookings_event_status_idx ON bookings (event_id, status);

ALTER TABLE payments
    -- Reconciliation asks Daraja about a pending payment a bounded number of times.
    ADD COLUMN last_queried_at TIMESTAMPTZ,
    ADD COLUMN query_attempts  INTEGER NOT NULL DEFAULT 0 CHECK (query_attempts >= 0);
-- Two live STK pushes for one booking is how a fan gets charged twice.
CREATE UNIQUE INDEX payments_live_booking_idx ON payments (booking_id)
    WHERE status IN ('INITIATED', 'PENDING');
-- One M-Pesa receipt settles one payment.
CREATE UNIQUE INDEX payments_mpesa_receipt_key ON payments (mpesa_receipt)
    WHERE mpesa_receipt IS NOT NULL;
-- "PENDING and older than 60s" is already served by payments_pending_idx from 000001.

-- Money that has to go back: a payment that arrived after its seats were resold, or for the
-- wrong amount (§8.2).
CREATE TABLE refunds (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    booking_id    BIGINT NOT NULL REFERENCES bookings (id),
    -- Unique so a callback processed twice queues one refund.
    payment_id    BIGINT NOT NULL UNIQUE REFERENCES payments (id),
    amount_cents  BIGINT NOT NULL CHECK (amount_cents > 0),
    reason        TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DONE')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,
    CHECK ((status = 'DONE') = (completed_at IS NOT NULL))
);
CREATE INDEX refunds_booking_id_idx ON refunds (booking_id);
CREATE INDEX refunds_pending_idx ON refunds (created_at) WHERE status = 'PENDING';

CREATE TABLE organiser_alerts (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id         BIGINT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    booking_id       BIGINT REFERENCES bookings (id),
    kind             TEXT NOT NULL
                     CHECK (kind IN ('REFUND_REQUIRED', 'AMOUNT_MISMATCH', 'PAYMENT_UNRESOLVED')),
    message          TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    acknowledged_at  TIMESTAMPTZ
);
CREATE INDEX organiser_alerts_event_created_idx ON organiser_alerts (event_id, created_at DESC);
CREATE INDEX organiser_alerts_open_idx ON organiser_alerts (event_id) WHERE acknowledged_at IS NULL;

ALTER TABLE tickets
    ADD COLUMN event_id   BIGINT NOT NULL REFERENCES events (id),
    -- "<stand code>-<tier>", the gates this ticket may enter by (correction 8).
    ADD COLUMN gate_group TEXT NOT NULL CHECK (gate_group ~ '^[A-Z0-9]+-(LOWER|UPPER)$'),
    -- Which signing key made the QR, so keys can be rotated without voiding old tickets.
    ADD COLUMN key_id     TEXT NOT NULL,
    -- Gate devices re-download only the manifest rows that changed.
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE TRIGGER tickets_set_updated_at BEFORE UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX tickets_event_updated_idx ON tickets (event_id, updated_at);

ALTER TABLE scan_logs
    ADD COLUMN event_id       BIGINT NOT NULL REFERENCES events (id),
    -- Made up by the device per scan. Replaces (ticket_id, device_id, scanned_at) as the
    -- re-upload guard, so there is exactly one rule deciding "this scan is already here".
    ADD COLUMN client_scan_id UUID NOT NULL,
    ADD CONSTRAINT scan_logs_client_scan_id_key UNIQUE (client_scan_id),
    DROP CONSTRAINT scan_logs_ticket_id_device_id_scanned_at_key,
    DROP CONSTRAINT scan_logs_result_check,
    ADD CONSTRAINT scan_logs_result_check
        CHECK (result IN ('ADMITTED', 'DUPLICATE', 'INVALID', 'WRONG_EVENT', 'WRONG_GATE'));
CREATE INDEX scan_logs_ticket_idx ON scan_logs (ticket_id, scanned_at);
CREATE INDEX scan_logs_event_scanned_idx ON scan_logs (event_id, scanned_at);
