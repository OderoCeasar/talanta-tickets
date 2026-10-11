DROP INDEX IF EXISTS holds_active_expiry_idx;
ALTER TABLE holds DROP COLUMN IF EXISTS extended_at;

DROP INDEX IF EXISTS event_seats_hold_id_idx;
DROP INDEX IF EXISTS event_seats_event_block_updated_idx;
DROP INDEX IF EXISTS event_seats_event_block_category_status_idx;
CREATE INDEX IF NOT EXISTS event_seats_event_block_status_idx ON event_seats (event_id, block_id, status);

DROP TRIGGER IF EXISTS event_seats_set_updated_at ON event_seats;

ALTER TABLE event_seats
    DROP CONSTRAINT IF EXISTS event_seats_event_category_fkey,
    DROP CONSTRAINT IF EXISTS event_seats_held_check,
    DROP CONSTRAINT IF EXISTS event_seats_priced_check,
    DROP COLUMN IF EXISTS updated_at;
ALTER TABLE event_seats
    ADD CONSTRAINT event_seats_category_id_fkey FOREIGN KEY (category_id) REFERENCES event_categories (id);
