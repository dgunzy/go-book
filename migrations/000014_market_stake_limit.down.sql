ALTER TABLE markets DROP CONSTRAINT IF EXISTS markets_max_stake_check;
ALTER TABLE markets DROP COLUMN IF EXISTS max_stake_cents;
