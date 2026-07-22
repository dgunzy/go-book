-- A match winner is represented by one active market. Terminal voided or
-- cancelled markets remain immutable history but do not block a replacement.
CREATE UNIQUE INDEX markets_one_active_match_market_idx
    ON markets (match_id)
    WHERE market_type = 'match' AND state NOT IN ('voided', 'cancelled');

