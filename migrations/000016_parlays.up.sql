-- Parlays: one stake riding on several match results at once.
--
-- A parlay cannot be a row in wagers. A wager belongs to exactly one market
-- and is graded when that market settles; a parlay spans markets and resolves
-- only once its last leg is in. It also cannot carry an American price: two
-- short favourites combine to less than even money, which American odds
-- cannot express, so the price is decimal odds in millionths.
CREATE TABLE parlays (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    funding_account_type text NOT NULL CHECK (funding_account_type IN ('user_cash', 'user_free_play')),
    stake_cents bigint NOT NULL CHECK (stake_cents > 0),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    -- Decimal odds x 1e6. Always strictly greater than 1.0 so a winning
    -- parlay pays something over the stake.
    accepted_price_micros bigint NOT NULL CHECK (accepted_price_micros > 1000000),
    potential_profit_cents bigint NOT NULL CHECK (potential_profit_cents > 0),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'rejected', 'settled', 'voided')),
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 200),
    placed_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    placed_at timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz,
    accepted_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    rejected_at timestamptz,
    rejected_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    rejection_reason text,
    acceptance_ledger_transaction_id uuid UNIQUE REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    UNIQUE (user_id, idempotency_key),
    CHECK ((state IN ('accepted', 'settled', 'voided') AND accepted_at IS NOT NULL AND acceptance_ledger_transaction_id IS NOT NULL) OR
           (state NOT IN ('accepted', 'settled', 'voided'))),
    CHECK ((state = 'rejected' AND rejected_at IS NOT NULL AND rejected_by IS NOT NULL AND length(btrim(rejection_reason)) > 0) OR state <> 'rejected')
);

-- One leg per market. The odds and terms are snapshots taken when the parlay
-- was placed, exactly as a single wager snapshots its price, so a later line
-- move cannot change what was struck.
CREATE TABLE parlay_legs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    parlay_id uuid NOT NULL REFERENCES parlays(id) ON DELETE RESTRICT,
    leg_index integer NOT NULL CHECK (leg_index >= 0),
    market_id uuid NOT NULL,
    selection_id uuid NOT NULL,
    accepted_american_odds integer NOT NULL CHECK (accepted_american_odds <= -100 OR accepted_american_odds >= 100),
    accepted_terms text NOT NULL CHECK (length(btrim(accepted_terms)) BETWEEN 1 AND 1000),
    -- Null until the leg's market settles. push and void drop the leg out of
    -- the parlay and the price is recomputed on what is left, which is how
    -- every book treats them.
    result text CHECK (result IN ('win', 'loss', 'push', 'void')),
    resolved_at timestamptz,
    FOREIGN KEY (market_id, selection_id) REFERENCES selections(market_id, id) ON DELETE RESTRICT,
    UNIQUE (parlay_id, leg_index),
    -- The same market may not appear twice in one parlay: correlated legs are
    -- how a book gets picked off.
    UNIQUE (parlay_id, market_id),
    CHECK ((result IS NULL AND resolved_at IS NULL) OR (result IS NOT NULL AND resolved_at IS NOT NULL))
);

CREATE INDEX parlay_legs_market_idx ON parlay_legs (market_id) WHERE result IS NULL;
CREATE INDEX parlays_user_idx ON parlays (user_id, placed_at DESC);
CREATE INDEX parlays_state_idx ON parlays (state);

-- What a settled parlay paid, mirroring wager_settlements so the two read the
-- same way in the record and in reconciliation.
CREATE TABLE parlay_settlements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    parlay_id uuid NOT NULL UNIQUE REFERENCES parlays(id) ON DELETE RESTRICT,
    result text NOT NULL CHECK (result IN ('win', 'loss', 'push', 'void')),
    -- The price actually paid, which is the placed price unless legs pushed
    -- out and the parlay repriced on the rest.
    settled_price_micros bigint NOT NULL CHECK (settled_price_micros >= 1000000),
    stake_cents bigint NOT NULL CHECK (stake_cents > 0),
    profit_cents bigint NOT NULL CHECK (profit_cents >= 0),
    returned_cents bigint NOT NULL CHECK (returned_cents >= 0),
    ledger_transaction_id uuid NOT NULL UNIQUE REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    settled_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((result = 'win' AND returned_cents = stake_cents + profit_cents) OR
           (result = 'loss' AND returned_cents = 0 AND profit_cents = 0) OR
           (result IN ('push', 'void') AND returned_cents = stake_cents AND profit_cents = 0))
);
