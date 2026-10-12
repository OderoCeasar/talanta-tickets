ALTER TABLE stands DROP CONSTRAINT IF EXISTS stands_code_check;

DROP INDEX IF EXISTS organiser_alerts_open_payment_idx;
ALTER TABLE organiser_alerts
    DROP COLUMN IF EXISTS resolution,
    DROP COLUMN IF EXISTS payment_id;

DROP TABLE IF EXISTS unmatched_callbacks;
DROP TABLE IF EXISTS payment_callbacks;

DROP INDEX IF EXISTS payments_idless_idx;
DROP INDEX IF EXISTS payments_timeout_requery_idx;
ALTER TABLE payments DROP COLUMN IF EXISTS dispatch;
