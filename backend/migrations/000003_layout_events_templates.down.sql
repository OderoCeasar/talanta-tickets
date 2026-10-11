DROP TABLE IF EXISTS venue_templates;
DROP TABLE IF EXISTS event_row_categories;
DROP TABLE IF EXISTS event_block_categories;

ALTER TABLE event_categories
    DROP CONSTRAINT IF EXISTS event_categories_color_hex_check,
    DROP CONSTRAINT IF EXISTS event_categories_event_id_id_key;

DROP TRIGGER IF EXISTS events_set_updated_at ON events;
ALTER TABLE events
    DROP COLUMN IF EXISTS published_at,
    DROP COLUMN IF EXISTS updated_at;

DROP INDEX IF EXISTS seats_row_id_active_idx;

ALTER TABLE blocks
    DROP CONSTRAINT IF EXISTS blocks_arc_radius_check,
    DROP CONSTRAINT IF EXISTS blocks_pitch_check,
    DROP CONSTRAINT IF EXISTS blocks_outline_check,
    DROP COLUMN IF EXISTS numbering_direction,
    DROP COLUMN IF EXISTS seats_per_row,
    DROP COLUMN IF EXISTS row_count,
    DROP COLUMN IF EXISTS outline;

ALTER TABLE venues DROP CONSTRAINT IF EXISTS venues_name_key;

DROP FUNCTION IF EXISTS set_updated_at();
