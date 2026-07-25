-- Restrict a member from one side of a market, not just the whole thing.
--
-- market_restrictions already banned a member from a market outright. A prop
-- often needs finer aim: the player the bet is about may take one side and not
-- the other — "Theriault cannot bet the under on his own eagles line" — while
-- the rest of the board stays open to them.
--
-- selection_id NULL keeps the existing meaning: banned from the whole market.
ALTER TABLE market_restrictions
    ADD COLUMN selection_id uuid;

ALTER TABLE market_restrictions
    ADD CONSTRAINT market_restrictions_selection_fkey
    FOREIGN KEY (market_id, selection_id) REFERENCES selections(market_id, id) ON DELETE CASCADE;

-- The primary key was (market_id, user_id), which allowed only one row per
-- member per market. A member may now be restricted from several sides, so the
-- uniqueness moves to two partial indexes: one whole-market ban, and one ban
-- per side.
ALTER TABLE market_restrictions DROP CONSTRAINT market_restrictions_pkey;

CREATE UNIQUE INDEX market_restrictions_market_user_unique
    ON market_restrictions (market_id, user_id) WHERE selection_id IS NULL;

CREATE UNIQUE INDEX market_restrictions_selection_unique
    ON market_restrictions (market_id, user_id, selection_id) WHERE selection_id IS NOT NULL;

CREATE INDEX market_restrictions_user_idx ON market_restrictions (user_id);
