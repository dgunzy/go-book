DROP INDEX IF EXISTS wagers_placed_by_idx;
ALTER TABLE wagers DROP COLUMN IF EXISTS placed_by;
