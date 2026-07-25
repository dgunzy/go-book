-- Cap how much one member may have riding on a market.
--
-- A novelty line is meant to be a bit of fun, not somewhere to put real money.
-- max_stake_cents is the most any single member may have on the market at
-- once, counting every pending and accepted wager they hold on it. NULL means
-- no limit, which is every market posted before this.
ALTER TABLE markets
    ADD COLUMN max_stake_cents bigint;

ALTER TABLE markets
    ADD CONSTRAINT markets_max_stake_check
    CHECK (max_stake_cents IS NULL OR max_stake_cents > 0);
