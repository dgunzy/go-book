-- Side-level bans cannot be represented once the column is gone. Drop them
-- rather than silently widening them into whole-market bans, which would
-- restrict members from bets they were allowed to take.
DELETE FROM market_restrictions WHERE selection_id IS NOT NULL;

DROP INDEX IF EXISTS market_restrictions_user_idx;
DROP INDEX IF EXISTS market_restrictions_selection_unique;
DROP INDEX IF EXISTS market_restrictions_market_user_unique;

ALTER TABLE market_restrictions DROP CONSTRAINT IF EXISTS market_restrictions_selection_fkey;
ALTER TABLE market_restrictions DROP COLUMN IF EXISTS selection_id;

ALTER TABLE market_restrictions ADD CONSTRAINT market_restrictions_pkey PRIMARY KEY (market_id, user_id);
