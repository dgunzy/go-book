DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM wagers) THEN
        RAISE EXCEPTION 'cannot roll back market currency migration after wagers exist';
    END IF;
END;
$$;

ALTER TABLE wagers DROP CONSTRAINT IF EXISTS wagers_market_currency_fkey;
ALTER TABLE markets DROP CONSTRAINT IF EXISTS markets_id_currency_unique;
ALTER TABLE markets DROP COLUMN IF EXISTS currency;
