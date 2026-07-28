-- Restores the pre-dead-heat settlement rules.
--
-- This down deliberately fails if any dead heat has actually been settled: a
-- row with divisor 2 cannot satisfy the restored CHECK, and dropping the
-- column first would quietly rewrite what those members were paid. A failed
-- down is the correct outcome there — the way back from a settled dead heat is
-- a new audited correction, not a schema rollback.

ALTER TABLE market_settlement_outcomes
    DROP CONSTRAINT market_settlement_outcomes_dead_heat_check,
    DROP COLUMN dead_heat_divisor;

ALTER TABLE wager_settlements
    DROP CONSTRAINT wager_settlements_amounts_check,
    ADD CONSTRAINT wager_settlements_check CHECK (
        (result = 'win' AND returned_cents = stake_cents + profit_cents) OR
        (result = 'loss' AND returned_cents = 0 AND profit_cents = 0) OR
        (result IN ('push', 'void') AND returned_cents = stake_cents AND profit_cents = 0)
    );

ALTER TABLE wager_settlements DROP COLUMN dead_heat_divisor;
