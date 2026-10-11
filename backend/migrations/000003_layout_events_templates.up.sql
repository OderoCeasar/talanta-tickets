-- Layout generation, category painting, venue templates and event change markers.

-- Shared by every table whose rows are polled with "changed since T".
-- clock_timestamp(), not now(): now() is the transaction's start time, so a transaction that
-- began before a poll but committed after it would carry a stamp the poller has already passed.
CREATE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;

-- The seeder and the admin site find a venue by name.
ALTER TABLE venues ADD CONSTRAINT venues_name_key UNIQUE (name);

ALTER TABLE blocks
    -- Polygon in venue plan coordinates, so the stand view can draw a block without loading
    -- its seats.
    ADD COLUMN outline             JSONB NOT NULL DEFAULT '[]',
    -- The inputs of seat generation, kept so "generate seats" can be re-run or shown again.
    ADD COLUMN row_count           INTEGER NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    ADD COLUMN seats_per_row       INTEGER NOT NULL DEFAULT 0 CHECK (seats_per_row >= 0),
    ADD COLUMN numbering_direction TEXT NOT NULL DEFAULT 'LTR'
                                   CHECK (numbering_direction IN ('LTR', 'RTL')),
    ADD CONSTRAINT blocks_outline_check CHECK (jsonb_typeof(outline) = 'array'),
    ADD CONSTRAINT blocks_pitch_check CHECK (seat_pitch_x > 0 AND seat_pitch_y > 0),
    ADD CONSTRAINT blocks_arc_radius_check CHECK (arc_radius IS NULL OR arc_radius > 0);

-- Publish joins seats to their row's category override.
CREATE INDEX seats_row_id_active_idx ON seats (row_id) WHERE is_active;

ALTER TABLE events
    ADD COLUMN updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN published_at TIMESTAMPTZ;
CREATE TRIGGER events_set_updated_at BEFORE UPDATE ON events
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Target of the composite foreign keys below: they make it impossible to price a seat with
-- another event's category.
ALTER TABLE event_categories
    ADD CONSTRAINT event_categories_event_id_id_key UNIQUE (event_id, id),
    ADD CONSTRAINT event_categories_color_hex_check CHECK (color_hex ~ '^#[0-9A-Fa-f]{6}$');

-- Category painting before publish. Publish resolves a seat's category as its row's override,
-- else its block's.
CREATE TABLE event_block_categories (
    event_id     BIGINT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    block_id     BIGINT NOT NULL REFERENCES blocks (id) ON DELETE CASCADE,
    category_id  BIGINT NOT NULL,
    PRIMARY KEY (event_id, block_id),
    FOREIGN KEY (event_id, category_id) REFERENCES event_categories (event_id, id) ON DELETE CASCADE
);
CREATE INDEX event_block_categories_category_idx ON event_block_categories (category_id);
CREATE INDEX event_block_categories_block_idx ON event_block_categories (block_id);

CREATE TABLE event_row_categories (
    event_id     BIGINT NOT NULL REFERENCES events (id) ON DELETE CASCADE,
    row_id       BIGINT NOT NULL REFERENCES rows (id) ON DELETE CASCADE,
    category_id  BIGINT NOT NULL,
    PRIMARY KEY (event_id, row_id),
    FOREIGN KEY (event_id, category_id) REFERENCES event_categories (event_id, id) ON DELETE CASCADE
);
CREATE INDEX event_row_categories_category_idx ON event_row_categories (category_id);
CREATE INDEX event_row_categories_row_idx ON event_row_categories (row_id);

-- A saved set of categories and their block/row assignments, so the next event takes minutes.
--   categories:  [{"name", "color_hex", "price_cents", "description"}]
--   assignments: {"blocks": [{"block_id", "category"}], "rows": [{"row_id", "category"}]}
-- Assignments name the category rather than an id because categories are per event and do not
-- exist until the template is applied.
CREATE TABLE venue_templates (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    venue_id     BIGINT NOT NULL REFERENCES venues (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    categories   JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(categories) = 'array'),
    assignments  JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(assignments) = 'object'),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (venue_id, name)
);
