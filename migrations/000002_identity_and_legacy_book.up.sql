CREATE TABLE oidc_login_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    state_hash bytea NOT NULL UNIQUE CHECK (octet_length(state_hash) >= 32),
    nonce_hash bytea NOT NULL CHECK (octet_length(nonce_hash) >= 32),
    pkce_verifier text NOT NULL CHECK (length(pkce_verifier) BETWEEN 43 AND 128),
    return_path text NOT NULL DEFAULT '/book' CHECK (
        length(return_path) BETWEEN 1 AND 2048 AND
        left(return_path, 1) = '/' AND
        left(return_path, 2) <> '//' AND
        position(E'\n' in return_path) = 0 AND
        position(E'\r' in return_path) = 0
    ),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);
CREATE INDEX oidc_login_attempts_expiry_idx ON oidc_login_attempts (expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE legacy_book_user_mappings (
    source_user_id bigint PRIMARY KEY CHECK (source_user_id > 0),
    migration_batch_id uuid NOT NULL REFERENCES migration_batches(id) ON DELETE RESTRICT,
    user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
    source_checksum text NOT NULL CHECK (length(source_checksum) = 64),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_cents bigint NOT NULL,
    free_play_balance_cents bigint NOT NULL,
    transaction_net_cents bigint NOT NULL,
    reconciliation_difference_cents bigint NOT NULL,
    import_state text NOT NULL CHECK (import_state IN ('staged', 'promoted', 'failed')),
    imported_at timestamptz,
    UNIQUE (migration_batch_id, source_user_id),
    CHECK (reconciliation_difference_cents = balance_cents - transaction_net_cents),
    CHECK ((import_state = 'promoted' AND imported_at IS NOT NULL) OR
           (import_state <> 'promoted' AND imported_at IS NULL))
);
CREATE INDEX legacy_book_user_mappings_user_idx ON legacy_book_user_mappings (user_id);

CREATE TABLE legacy_book_transactions (
    source_transaction_id bigint PRIMARY KEY CHECK (source_transaction_id > 0),
    migration_batch_id uuid NOT NULL REFERENCES migration_batches(id) ON DELETE RESTRICT,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount_cents bigint NOT NULL CHECK (amount_cents <> 0),
    transaction_type text NOT NULL CHECK (transaction_type IN ('credit', 'debit')),
    description text NOT NULL,
    occurred_at timestamptz NOT NULL,
    source_checksum text NOT NULL CHECK (length(source_checksum) = 64),
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (migration_batch_id, source_transaction_id),
    CHECK ((transaction_type = 'credit' AND amount_cents > 0) OR
           (transaction_type = 'debit' AND amount_cents < 0))
);
CREATE INDEX legacy_book_transactions_user_history_idx
    ON legacy_book_transactions (user_id, occurred_at DESC, source_transaction_id DESC);

CREATE TABLE legacy_book_wagers (
    source_wager_id bigint PRIMARY KEY CHECK (source_wager_id > 0),
    migration_batch_id uuid NOT NULL REFERENCES migration_batches(id) ON DELETE RESTRICT,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    stake_cents bigint NOT NULL CHECK (stake_cents > 0),
    accepted_terms text NOT NULL CHECK (length(btrim(accepted_terms)) > 0),
    accepted_american_odds integer NOT NULL CHECK (
        accepted_american_odds <= -100 OR accepted_american_odds >= 100
    ),
    placed_at timestamptz NOT NULL,
    result text NOT NULL CHECK (result IN ('win', 'loss', 'push')),
    approved boolean NOT NULL,
    source_checksum text NOT NULL CHECK (length(source_checksum) = 64),
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (migration_batch_id, source_wager_id)
);
CREATE INDEX legacy_book_wagers_user_history_idx
    ON legacy_book_wagers (user_id, placed_at DESC, source_wager_id DESC);

CREATE OR REPLACE FUNCTION reject_legacy_book_history_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'legacy book history is immutable';
END;
$$;

CREATE TRIGGER legacy_book_transactions_immutable
BEFORE UPDATE OR DELETE ON legacy_book_transactions
FOR EACH ROW EXECUTE FUNCTION reject_legacy_book_history_mutation();

CREATE TRIGGER legacy_book_user_mappings_immutable
BEFORE UPDATE OR DELETE ON legacy_book_user_mappings
FOR EACH ROW EXECUTE FUNCTION reject_legacy_book_history_mutation();

CREATE TRIGGER legacy_book_wagers_immutable
BEFORE UPDATE OR DELETE ON legacy_book_wagers
FOR EACH ROW EXECUTE FUNCTION reject_legacy_book_history_mutation();

CREATE VIEW ledger_account_balances AS
SELECT a.id AS account_id,
       a.owner_user_id,
       a.account_type,
       a.currency,
       COALESCE(sum(p.amount_cents), 0)::bigint AS balance_cents
  FROM ledger_accounts a
  LEFT JOIN ledger_postings p ON p.account_id = a.id
 GROUP BY a.id, a.owner_user_id, a.account_type, a.currency;
