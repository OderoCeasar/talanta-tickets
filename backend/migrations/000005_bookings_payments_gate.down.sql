DROP INDEX IF EXISTS scan_logs_event_scanned_idx;
DROP INDEX IF EXISTS scan_logs_ticket_idx;
-- The old check has no WRONG_GATE, so those scans cannot survive the way back.
DELETE FROM scan_logs WHERE result = 'WRONG_GATE';
ALTER TABLE scan_logs
    DROP CONSTRAINT IF EXISTS scan_logs_result_check,
    DROP CONSTRAINT IF EXISTS scan_logs_client_scan_id_key,
    DROP COLUMN IF EXISTS client_scan_id,
    DROP COLUMN IF EXISTS event_id;
ALTER TABLE scan_logs
    ADD CONSTRAINT scan_logs_result_check
        CHECK (result IN ('ADMITTED', 'DUPLICATE', 'INVALID', 'WRONG_EVENT')),
    ADD CONSTRAINT scan_logs_ticket_id_device_id_scanned_at_key UNIQUE (ticket_id, device_id, scanned_at);

DROP INDEX IF EXISTS tickets_event_updated_idx;
DROP TRIGGER IF EXISTS tickets_set_updated_at ON tickets;
ALTER TABLE tickets
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS key_id,
    DROP COLUMN IF EXISTS gate_group,
    DROP COLUMN IF EXISTS event_id;

DROP TABLE IF EXISTS organiser_alerts;
DROP TABLE IF EXISTS refunds;

DROP INDEX IF EXISTS payments_mpesa_receipt_key;
DROP INDEX IF EXISTS payments_live_booking_idx;
ALTER TABLE payments
    DROP COLUMN IF EXISTS query_attempts,
    DROP COLUMN IF EXISTS last_queried_at;

DROP INDEX IF EXISTS bookings_event_status_idx;
DROP INDEX IF EXISTS bookings_hold_id_idx;
DROP TRIGGER IF EXISTS bookings_set_updated_at ON bookings;
ALTER TABLE bookings
    DROP CONSTRAINT IF EXISTS bookings_total_check,
    DROP CONSTRAINT IF EXISTS bookings_web_token_key,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS web_token;
