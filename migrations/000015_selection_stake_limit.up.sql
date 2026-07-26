-- Cap a member's stake on one side of a market, not just across the whole of it.
--
-- markets.max_stake_cents caps what a member may have on the market in total.
-- That is the wrong shape for a lopsided prop: you may be happy to take real
-- money on the -1200 side while holding the +750 side to $50. This column caps
-- one selection; NULL means the side is limited only by the market's own cap,
-- if it has one.
--
-- Both limits apply when both are set. Nothing changes for markets that do not
-- use this: the market-level cap keeps the meaning it already had.
ALTER TABLE selections
    ADD COLUMN max_stake_cents bigint;

ALTER TABLE selections
    ADD CONSTRAINT selections_max_stake_check
    CHECK (max_stake_cents IS NULL OR max_stake_cents > 0);
