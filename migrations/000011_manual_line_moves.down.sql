ALTER TABLE selection_price_changes DROP CONSTRAINT IF EXISTS selection_price_changes_manual_reason_check;
ALTER TABLE selection_price_changes DROP COLUMN IF EXISTS reason;
ALTER TABLE selection_price_changes DROP COLUMN IF EXISTS actor_user_id;
