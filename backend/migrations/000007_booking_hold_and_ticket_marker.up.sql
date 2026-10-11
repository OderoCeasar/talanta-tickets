-- Two fixes found in review of 000004 and 000005.

-- One hold can back only one live booking. Without this, a fan who taps Pay on two phones
-- (two idempotency keys) gets two PENDING_PAYMENT bookings and two STK pushes for the same
-- seats: the second payment cannot sell anything and ends as a refund.
CREATE UNIQUE INDEX bookings_live_hold_key ON bookings (hold_id)
    WHERE status IN ('PENDING_PAYMENT', 'PAID');

-- A new ticket must carry the time it was written, not the time its transaction began.
-- The column default is now(), which is the transaction start: a gate manifest poll that ran
-- between the start and the commit of a payment would never see that ticket.
DROP TRIGGER tickets_set_updated_at ON tickets;
CREATE TRIGGER tickets_set_updated_at BEFORE INSERT OR UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
