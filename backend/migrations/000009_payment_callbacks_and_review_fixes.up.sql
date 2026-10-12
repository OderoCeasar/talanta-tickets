-- Fixes from the review of the money path. The rule behind all of it: once there is any
-- evidence that money moved for a payment, the payment ends with tickets or with a refund
-- row and an alert, never with neither.

-- What we know about whether the STK push reached M-Pesa.
--   UNKNOWN    nothing was recorded (the process died around the call): it may have been sent.
--   NOT_SENT   provably never left us (no connection, an explicit refusal).
--   AMBIGUOUS  sent, or possibly sent, and the answer was lost: M-Pesa may have prompted the
--              fan under an ID we never learned.
--   ACCEPTED   M-Pesa took it and its IDs are on the row.
-- A payment with no checkout_request_id that is not NOT_SENT is closed with an alert, and is
-- what a callback with an unknown ID is matched against.
ALTER TABLE payments
    ADD COLUMN dispatch TEXT NOT NULL DEFAULT 'UNKNOWN'
        CHECK (dispatch IN ('UNKNOWN', 'NOT_SENT', 'AMBIGUOUS', 'ACCEPTED'));
UPDATE payments SET dispatch = 'ACCEPTED' WHERE checkout_request_id IS NOT NULL;
-- Reconciliation keeps asking M-Pesa about TIMEOUT payments on a slow schedule.
CREATE INDEX payments_timeout_requery_idx ON payments (last_queried_at)
    WHERE status = 'TIMEOUT' AND checkout_request_id IS NOT NULL;
-- Matching a callback with an unknown ID to a payment whose ID we never learned.
CREATE INDEX payments_idless_idx ON payments (phone_e164, amount_cents, created_at)
    WHERE checkout_request_id IS NULL;

-- The callback inbox. The webhook writes a callback here BEFORE it answers 200, so the
-- report survives a busy event, a database error, a full queue or a restart; a worker job
-- retries every row that has not been dealt with.
CREATE TABLE payment_callbacks (
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    payment_id           BIGINT NOT NULL REFERENCES payments (id),
    checkout_request_id  TEXT NOT NULL,
    merchant_request_id  TEXT,
    result_code          INTEGER NOT NULL,
    result_desc          TEXT,
    -- What the body claims. Never used as the amount of money that moved.
    amount_cents         BIGINT,
    mpesa_receipt        TEXT,
    phone_e164           TEXT,
    raw                  JSONB NOT NULL,
    received_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    attempts             INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    processed_at         TIMESTAMPTZ,
    -- APPLIED: put through the finishing function. REFUTED: M-Pesa's own answer contradicts
    -- the body. DEFERRED: not a final result; reconciliation owns the payment. ABANDONED: could
    -- not be dealt with in a week (an alert was raised).
    verdict              TEXT CHECK (verdict IN ('APPLIED', 'REFUTED', 'DEFERRED', 'ABANDONED')),
    last_error           TEXT,
    CHECK ((processed_at IS NULL) = (verdict IS NULL)),
    -- One row per (payment, result): a repeat re-arms the row instead of adding one.
    CONSTRAINT payment_callbacks_payment_result_key UNIQUE (payment_id, result_code)
);
CREATE INDEX payment_callbacks_due_idx ON payment_callbacks (next_attempt_at) WHERE processed_at IS NULL;

-- "Paid" callbacks from an allowed address whose CheckoutRequestID matches no payment: the
-- STK push reached M-Pesa but its answer never reached us. Kept so the money can be found
-- and refunded by a person, and matched to an ID-less payment when exactly one fits.
CREATE TABLE unmatched_callbacks (
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    checkout_request_id  TEXT NOT NULL,
    merchant_request_id  TEXT,
    result_code          INTEGER NOT NULL,
    result_desc          TEXT,
    amount_cents         BIGINT,
    mpesa_receipt        TEXT,
    phone_e164           TEXT,
    raw                  JSONB NOT NULL,
    received_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    attempts             INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    processed_at         TIMESTAMPTZ,
    -- MATCHED: M-Pesa confirmed it and it was tied to matched_payment_id. REFUTED: M-Pesa
    -- says this ID was not paid. UNMATCHED: no payment could be found for it in time.
    verdict              TEXT CHECK (verdict IN ('MATCHED', 'REFUTED', 'UNMATCHED')),
    matched_payment_id   BIGINT REFERENCES payments (id),
    alert_id             BIGINT REFERENCES organiser_alerts (id) ON DELETE SET NULL,
    last_error           TEXT,
    CHECK ((processed_at IS NULL) = (verdict IS NULL)),
    CHECK ((verdict IS NOT DISTINCT FROM 'MATCHED') = (matched_payment_id IS NOT NULL)),
    CONSTRAINT unmatched_callbacks_checkout_request_id_key UNIQUE (checkout_request_id)
);
CREATE INDEX unmatched_callbacks_due_idx ON unmatched_callbacks (next_attempt_at) WHERE processed_at IS NULL;

-- An alert about a payment names the payment, so it can be closed by the system when the
-- payment resolves: a PAYMENT_UNRESOLVED alert when the payment is later sold or queued for
-- refund, a REFUND_REQUIRED or AMOUNT_MISMATCH alert when its refund is marked as paid back.
-- resolution says, in words, that the system closed it and why; NULL for an alert a person
-- acknowledged (or nobody has yet).
ALTER TABLE organiser_alerts
    ADD COLUMN payment_id BIGINT REFERENCES payments (id),
    ADD COLUMN resolution TEXT;
CREATE INDEX organiser_alerts_open_payment_idx ON organiser_alerts (payment_id)
    WHERE payment_id IS NOT NULL AND acknowledged_at IS NULL;

-- A ticket's gate group is "<stand code>-<tier>": it must match ^[A-Z0-9]+-(LOWER|UPPER)$ and
-- fit the 16 bytes the signed token gives it, which leaves the code ten characters. A stand
-- whose code breaks this makes every ticket of the stand impossible to sign. The seeded codes
-- are N, S, E and W; a stand table is a handful of rows, so the check is validated at once.
ALTER TABLE stands
    ADD CONSTRAINT stands_code_check CHECK (code ~ '^[A-Z0-9]{1,10}$');
