-- Dead heats: several selections tie for a win.
--
-- A dead heat is not a new result. The winners still won, so the result stays
-- 'win'; what changes is how much of the stake is riding on it. With N tied
-- winners only stake/N wins at the accepted odds and the rest loses, which is
-- how every book has settled a tie since long before there were books to
-- settle it. The divisor is recorded rather than inferred so that a regrade
-- replays the same money: bettingweb replays a market's own recorded outcome,
-- and a divisor it could not read back would silently repay these wagers at
-- full odds.
--
-- Existing rows all carry divisor 1, and at divisor 1 both CHECKs below reduce
-- to exactly the rule they replace, so nothing already settled changes.

ALTER TABLE wager_settlements
    ADD COLUMN dead_heat_divisor integer NOT NULL DEFAULT 1
        CHECK (dead_heat_divisor >= 1);

-- Integer division here is deliberate and matches the Go side exactly: the
-- fraction of a cent that will not divide is kept by the house rather than
-- invented for one of the winners.
ALTER TABLE wager_settlements
    DROP CONSTRAINT wager_settlements_check,
    ADD CONSTRAINT wager_settlements_amounts_check CHECK (
        (result = 'win' AND returned_cents = stake_cents / dead_heat_divisor + profit_cents) OR
        (result = 'loss' AND returned_cents = 0 AND profit_cents = 0) OR
        (result IN ('push', 'void') AND returned_cents = stake_cents AND profit_cents = 0)
    );

-- The divisor belongs to the recorded outcome too, so a regrade of a dead-heat
-- market grades the stranded wagers the same way the first pass did.
ALTER TABLE market_settlement_outcomes
    ADD COLUMN dead_heat_divisor integer NOT NULL DEFAULT 1
        CHECK (dead_heat_divisor >= 1),
    ADD CONSTRAINT market_settlement_outcomes_dead_heat_check
        CHECK (dead_heat_divisor = 1 OR outcome = 'win');
