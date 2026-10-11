DROP TRIGGER tickets_set_updated_at ON tickets;
CREATE TRIGGER tickets_set_updated_at BEFORE UPDATE ON tickets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP INDEX bookings_live_hold_key;
