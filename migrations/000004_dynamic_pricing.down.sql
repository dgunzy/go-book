DROP TRIGGER IF EXISTS selection_price_changes_immutable ON selection_price_changes;
DROP FUNCTION IF EXISTS reject_price_change_mutation();
DROP TABLE IF EXISTS selection_price_changes;

ALTER TABLE markets DROP CONSTRAINT IF EXISTS markets_pricing_liquidity_check;
ALTER TABLE markets DROP COLUMN IF EXISTS pricing_liquidity_cents;
ALTER TABLE markets DROP COLUMN IF EXISTS dynamic_pricing;

DROP TRIGGER IF EXISTS selections_default_opening_odds ON selections;
DROP FUNCTION IF EXISTS default_selection_opening_odds();
ALTER TABLE selections DROP CONSTRAINT IF EXISTS selections_opening_odds_range;
ALTER TABLE selections DROP COLUMN IF EXISTS opening_american_odds;
