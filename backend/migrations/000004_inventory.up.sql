-- event_seats is the hot table: change marker, indexes for the real queries, and two rules the
-- database can enforce on its own.

ALTER TABLE event_seats
    -- Correction 9: the seat map polls "seats in this block changed since T".
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A seat nobody priced must not be sellable. Publish inserts unpainted seats as BLOCKED.
    ADD CONSTRAINT event_seats_priced_check CHECK (status = 'BLOCKED' OR category_id IS NOT NULL),
    -- A held seat always knows which hold owns it and when that hold lapses; correction 5 sells
    -- seats by hold_id, so a HELD row without one could never be sold or swept.
    ADD CONSTRAINT event_seats_held_check
        CHECK (status <> 'HELD' OR (hold_id IS NOT NULL AND hold_expires_at IS NOT NULL)),
    -- Replaces the single-column foreign key: the category must belong to the same event.
    DROP CONSTRAINT event_seats_category_id_fkey,
    ADD CONSTRAINT event_seats_event_category_fkey
        FOREIGN KEY (event_id, category_id) REFERENCES event_categories (event_id, id);

-- Set by the database, not by each statement, so no writer can forget it.
CREATE TRIGGER event_seats_set_updated_at BEFORE UPDATE ON event_seats
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One index for two queries: the block seat list (event_id, block_id prefix) and the
-- availability counts per block and category, which it answers without touching the table.
-- It supersedes (event_id, block_id, status); every index here is rewritten on each status
-- change, so the narrower one is dropped rather than kept beside it.
DROP INDEX event_seats_event_block_status_idx;
CREATE INDEX event_seats_event_block_category_status_idx
    ON event_seats (event_id, block_id, category_id, status);

CREATE INDEX event_seats_event_block_updated_idx ON event_seats (event_id, block_id, updated_at);

-- Selling and releasing address seats by their hold (correction 5).
CREATE INDEX event_seats_hold_id_idx ON event_seats (hold_id) WHERE hold_id IS NOT NULL;

-- Correction 4: a hold is pushed out once when payment starts, never twice.
ALTER TABLE holds ADD COLUMN extended_at TIMESTAMPTZ;
-- The sweeper closes lapsed holds (correction 7), and the per-user limit counts live ones.
CREATE INDEX holds_active_expiry_idx ON holds (expires_at) WHERE status = 'ACTIVE';
