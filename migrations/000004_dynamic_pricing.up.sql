-- Dynamic (exposure-based) line movement.
--
-- offered_american_odds is the live line a new bet is quoted at.
-- opening_american_odds is the stable prior the pricing engine reprices from,
-- so repeated repricing never drifts: the current line is always a
-- deterministic function of (opening line, accumulated exposure, liquidity),
-- not of the previous output. It is backfilled from the current offered line
-- for existing selections.
ALTER TABLE selections
    ADD COLUMN opening_american_odds integer;

UPDATE selections SET opening_american_odds = offered_american_odds;

ALTER TABLE selections
    ALTER COLUMN opening_american_odds SET NOT NULL;

ALTER TABLE selections
    ADD CONSTRAINT selections_opening_odds_range
    CHECK (opening_american_odds <= -100 OR opening_american_odds >= 100);

-- The opening line is, by definition, the first offered line. Any insert that
-- omits it inherits the offered odds, so callers cannot forget the prior and
-- the value can never be null.
CREATE OR REPLACE FUNCTION default_selection_opening_odds() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.opening_american_odds IS NULL THEN
        NEW.opening_american_odds := NEW.offered_american_odds;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER selections_default_opening_odds
BEFORE INSERT ON selections
FOR EACH ROW EXECUTE FUNCTION default_selection_opening_odds();

-- Per-market pricing controls. dynamic_pricing gates whether the line moves at
-- all; pricing_liquidity_cents is the "b" sensitivity (larger = smaller moves)
-- and must be present and positive when pricing is enabled.
ALTER TABLE markets
    ADD COLUMN dynamic_pricing boolean NOT NULL DEFAULT false;

ALTER TABLE markets
    ADD COLUMN pricing_liquidity_cents bigint;

ALTER TABLE markets
    ADD CONSTRAINT markets_pricing_liquidity_check
    CHECK ((dynamic_pricing = false) OR (pricing_liquidity_cents IS NOT NULL AND pricing_liquidity_cents > 0));

-- Immutable audit trail of every automatic line move, one row per selection
-- per reprice, keyed to the wager whose acceptance triggered it.
CREATE TABLE selection_price_changes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id uuid NOT NULL REFERENCES markets(id) ON DELETE RESTRICT,
    selection_id uuid NOT NULL REFERENCES selections(id) ON DELETE RESTRICT,
    trigger_wager_id uuid REFERENCES wagers(id) ON DELETE RESTRICT,
    old_american_odds integer NOT NULL CHECK (old_american_odds <= -100 OR old_american_odds >= 100),
    new_american_odds integer NOT NULL CHECK (new_american_odds <= -100 OR new_american_odds >= 100),
    exposure_cents bigint NOT NULL CHECK (exposure_cents >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX selection_price_changes_market_idx ON selection_price_changes (market_id, created_at DESC);
CREATE INDEX selection_price_changes_selection_idx ON selection_price_changes (selection_id, created_at DESC);

CREATE OR REPLACE FUNCTION reject_price_change_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'selection_price_changes is append-only';
END;
$$;
CREATE TRIGGER selection_price_changes_immutable
BEFORE UPDATE OR DELETE ON selection_price_changes
FOR EACH ROW EXECUTE FUNCTION reject_price_change_mutation();
