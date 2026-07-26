ALTER TABLE selections DROP CONSTRAINT IF EXISTS selections_max_stake_check;
ALTER TABLE selections DROP COLUMN IF EXISTS max_stake_cents;
