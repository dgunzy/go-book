-- selections.max_stake_cents caps what ONE member may have on a side. Nothing
-- capped what the whole book could take on it, so ten members at the per-member
-- limit was ten times the intended liability.
--
-- Null means no cap, matching max_stake_cents. Pending wagers count toward it:
-- otherwise the cap can be walked past with unapproved wagers and then breached
-- the moment they are approved.
ALTER TABLE selections ADD COLUMN total_stake_cap_cents bigint;

ALTER TABLE selections ADD CONSTRAINT selections_total_stake_cap_check
    CHECK (total_stake_cap_cents IS NULL OR total_stake_cap_cents > 0);
