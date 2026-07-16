CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 120),
    email text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('invited', 'active', 'suspended', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (email IS NULL OR email = lower(btrim(email)))
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email)) WHERE email IS NOT NULL;

CREATE TABLE auth_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_-]{1,31}$'),
    subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
    email text,
    email_verified boolean NOT NULL DEFAULT false,
    profile jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(profile) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_authenticated_at timestamptz,
    UNIQUE (provider, subject),
    CHECK (email IS NULL OR email = lower(btrim(email)))
);
CREATE INDEX auth_identities_user_idx ON auth_identities (user_id);

CREATE TABLE memberships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    role text NOT NULL CHECK (role IN ('member', 'admin', 'owner')),
    granted_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    granted_at timestamptz NOT NULL DEFAULT now(),
    revoked_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    revoked_at timestamptz,
    revocation_reason text,
    CHECK ((revoked_at IS NULL AND revoked_by IS NULL AND revocation_reason IS NULL) OR
           (revoked_at IS NOT NULL AND revoked_by IS NOT NULL AND length(btrim(revocation_reason)) > 0)),
    CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);
CREATE UNIQUE INDEX memberships_one_active_role_per_user ON memberships (user_id) WHERE revoked_at IS NULL;

CREATE TABLE invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) >= 32),
    intended_email text,
    role text NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'admin')),
    issued_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    consumed_by uuid UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
    revoked_at timestamptz,
    revoked_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (intended_email IS NULL OR intended_email = lower(btrim(intended_email))),
    CHECK (expires_at > created_at),
    CHECK ((consumed_at IS NULL) = (consumed_by IS NULL)),
    CHECK (consumed_at IS NULL OR consumed_at <= expires_at),
    CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
    CHECK (NOT (consumed_at IS NOT NULL AND revoked_at IS NOT NULL))
);
CREATE INDEX invitations_available_idx ON invitations (expires_at) WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) >= 32),
    csrf_secret_hash bytea NOT NULL CHECK (octet_length(csrf_secret_hash) >= 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    revoke_reason text,
    rotated_from uuid UNIQUE REFERENCES sessions(id) ON DELETE RESTRICT,
    CHECK (expires_at > created_at),
    CHECK (last_seen_at >= created_at),
    CHECK ((revoked_at IS NULL AND revoke_reason IS NULL) OR
           (revoked_at IS NOT NULL AND length(btrim(revoke_reason)) > 0))
);
CREATE INDEX sessions_user_active_idx ON sessions (user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE players (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    linked_user_id uuid UNIQUE REFERENCES users(id) ON DELETE SET NULL,
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 120),
    biography text,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE player_aliases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    alias text NOT NULL CHECK (length(btrim(alias)) BETWEEN 1 AND 120),
    normalized_alias text NOT NULL UNIQUE CHECK (normalized_alias = lower(btrim(normalized_alias))),
    source text NOT NULL DEFAULT 'manual',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
    season_year integer NOT NULL CHECK (season_year BETWEEN 2010 AND 2200),
    venue text NOT NULL CHECK (length(btrim(venue)) BETWEEN 1 AND 200),
    format_description text,
    narrative text,
    state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'scheduled', 'active', 'completed', 'cancelled')),
    starts_on date,
    ends_on date,
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (season_year, name),
    CHECK (ends_on IS NULL OR starts_on IS NULL OR ends_on >= starts_on)
);

CREATE TABLE teams (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
    captain_player_id uuid REFERENCES players(id) ON DELETE RESTRICT,
    color text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (event_id, slug),
    UNIQUE (event_id, id)
);

CREATE TABLE event_team_memberships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    team_id uuid NOT NULL,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    is_captain boolean NOT NULL DEFAULT false,
    joined_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (event_id, team_id) REFERENCES teams(event_id, id) ON DELETE CASCADE,
    UNIQUE (event_id, player_id),
    UNIQUE (team_id, player_id)
);

CREATE TABLE matches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    match_number integer NOT NULL CHECK (match_number > 0),
    format text NOT NULL CHECK (format IN ('singles', 'fourball', 'foursomes', 'scramble', 'other')),
    label text,
    state text NOT NULL DEFAULT 'scheduled' CHECK (state IN ('scheduled', 'open', 'pending_verification', 'verified', 'disputed', 'cancelled')),
    scheduled_at timestamptz,
    opened_at timestamptz,
    closed_at timestamptz,
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (event_id, match_number),
    UNIQUE (event_id, id),
    CHECK (closed_at IS NULL OR opened_at IS NULL OR closed_at >= opened_at)
);
CREATE INDEX matches_event_state_idx ON matches (event_id, state, match_number);

CREATE TABLE match_sides (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL,
    match_id uuid NOT NULL,
    side_number smallint NOT NULL CHECK (side_number IN (1, 2)),
    team_id uuid NOT NULL,
    FOREIGN KEY (event_id, match_id) REFERENCES matches(event_id, id) ON DELETE CASCADE,
    FOREIGN KEY (event_id, team_id) REFERENCES teams(event_id, id) ON DELETE RESTRICT,
    UNIQUE (match_id, side_number),
    UNIQUE (match_id, team_id),
    UNIQUE (match_id, id)
);

CREATE TABLE match_participants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id uuid NOT NULL,
    match_side_id uuid NOT NULL,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    playing_order smallint NOT NULL DEFAULT 1 CHECK (playing_order > 0),
    FOREIGN KEY (match_id, match_side_id) REFERENCES match_sides(match_id, id) ON DELETE CASCADE,
    UNIQUE (match_id, player_id),
    UNIQUE (match_side_id, playing_order)
);

CREATE TABLE result_submissions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id uuid NOT NULL REFERENCES matches(id) ON DELETE RESTRICT,
    submitted_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    side_1_points numeric(6,2) NOT NULL CHECK (side_1_points >= 0),
    side_2_points numeric(6,2) NOT NULL CHECK (side_2_points >= 0),
    outcome text NOT NULL CHECK (outcome IN ('side_1', 'side_2', 'tie', 'void')),
    note text,
    evidence jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(evidence) = 'array'),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'confirmed', 'rejected', 'disputed', 'superseded')),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolved_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    resolution_reason text,
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 200),
    UNIQUE (submitted_by, idempotency_key),
    CHECK ((resolved_at IS NULL AND resolved_by IS NULL AND resolution_reason IS NULL) OR
           (resolved_at IS NOT NULL AND resolved_by IS NOT NULL AND length(btrim(resolution_reason)) > 0)),
    CHECK ((outcome = 'side_1' AND side_1_points > side_2_points) OR
           (outcome = 'side_2' AND side_2_points > side_1_points) OR
           (outcome = 'tie' AND side_1_points = side_2_points) OR
           (outcome = 'void' AND side_1_points = 0 AND side_2_points = 0))
);
CREATE INDEX result_submissions_match_state_idx ON result_submissions (match_id, state, submitted_at);

CREATE TABLE result_confirmations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    submission_id uuid NOT NULL REFERENCES result_submissions(id) ON DELETE RESTRICT,
    confirmed_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    decision text NOT NULL CHECK (decision IN ('confirm', 'reject', 'dispute')),
    comment text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (decision = 'confirm' OR length(btrim(comment)) > 0),
    UNIQUE (submission_id, confirmed_by)
);

CREATE TABLE verified_results (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    match_id uuid NOT NULL REFERENCES matches(id) ON DELETE RESTRICT,
    version integer NOT NULL CHECK (version > 0),
    submission_id uuid REFERENCES result_submissions(id) ON DELETE RESTRICT,
    supersedes_result_id uuid UNIQUE,
    side_1_points numeric(6,2) NOT NULL CHECK (side_1_points >= 0),
    side_2_points numeric(6,2) NOT NULL CHECK (side_2_points >= 0),
    outcome text NOT NULL CHECK (outcome IN ('side_1', 'side_2', 'tie', 'void')),
    verification_method text NOT NULL CHECK (verification_method IN ('opponent', 'captain', 'admin_override', 'migration')),
    verified_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    verification_reason text,
    verified_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (match_id, version),
    UNIQUE (match_id, id),
    FOREIGN KEY (match_id, supersedes_result_id) REFERENCES verified_results(match_id, id) ON DELETE RESTRICT,
    CHECK ((version = 1 AND supersedes_result_id IS NULL) OR (version > 1 AND supersedes_result_id IS NOT NULL)),
    CHECK (verification_method <> 'admin_override' OR (verified_by IS NOT NULL AND length(btrim(verification_reason)) > 0)),
    CHECK ((outcome = 'side_1' AND side_1_points > side_2_points) OR
           (outcome = 'side_2' AND side_2_points > side_1_points) OR
           (outcome = 'tie' AND side_1_points = side_2_points) OR
           (outcome = 'void' AND side_1_points = 0 AND side_2_points = 0))
);
CREATE INDEX verified_results_match_latest_idx ON verified_results (match_id, version DESC);

CREATE TABLE migration_batches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_system text NOT NULL CHECK (length(btrim(source_system)) > 0),
    source_version text,
    state text NOT NULL DEFAULT 'staged' CHECK (state IN ('staged', 'validated', 'promoted', 'failed', 'rolled_back')),
    source_counts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(source_counts) = 'object'),
    reconciliation jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(reconciliation) = 'object'),
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (source_system, source_version)
);

CREATE TABLE legacy_import_records (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    migration_batch_id uuid NOT NULL REFERENCES migration_batches(id) ON DELETE RESTRICT,
    source_table text NOT NULL,
    source_primary_key text NOT NULL,
    target_table text,
    target_id uuid,
    source_checksum text NOT NULL,
    import_state text NOT NULL CHECK (import_state IN ('staged', 'imported', 'skipped', 'failed')),
    error_message text,
    imported_at timestamptz,
    UNIQUE (migration_batch_id, source_table, source_primary_key)
);

CREATE TABLE legacy_stat_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    migration_batch_id uuid NOT NULL REFERENCES migration_batches(id) ON DELETE RESTRICT,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    as_of_label text NOT NULL,
    source text NOT NULL,
    statistics jsonb NOT NULL CHECK (jsonb_typeof(statistics) = 'object'),
    imported_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (migration_batch_id, player_id, as_of_label)
);

CREATE TABLE player_stat_projections (
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    matches_played integer NOT NULL DEFAULT 0 CHECK (matches_played >= 0),
    wins integer NOT NULL DEFAULT 0 CHECK (wins >= 0),
    losses integer NOT NULL DEFAULT 0 CHECK (losses >= 0),
    ties integer NOT NULL DEFAULT 0 CHECK (ties >= 0),
    points_won numeric(10,2) NOT NULL DEFAULT 0 CHECK (points_won >= 0),
    projection_version bigint NOT NULL DEFAULT 0 CHECK (projection_version >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (player_id, event_id),
    CHECK (wins + losses + ties <= matches_played)
);

CREATE TABLE media_assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    object_key text NOT NULL UNIQUE CHECK (object_key ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,1023}$' AND object_key !~ '(^|/)\.\.(/|$)'),
    content_type text NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp', 'image/avif')),
    byte_size bigint NOT NULL CHECK (byte_size > 0 AND byte_size <= 52428800),
    checksum_sha256 text NOT NULL CHECK (checksum_sha256 ~ '^[a-f0-9]{64}$'),
    width integer CHECK (width > 0),
    height integer CHECK (height > 0),
    caption text,
    alt_text text NOT NULL CHECK (length(btrim(alt_text)) BETWEEN 1 AND 500),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'validated', 'published', 'rejected', 'archived')),
    uploaded_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    validated_at timestamptz,
    published_at timestamptz
);
CREATE INDEX media_assets_public_idx ON media_assets (published_at DESC) WHERE state = 'published';

CREATE TABLE media_event_links (
    media_asset_id uuid NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    display_order integer NOT NULL DEFAULT 0 CHECK (display_order >= 0),
    PRIMARY KEY (media_asset_id, event_id)
);
CREATE TABLE media_match_links (
    media_asset_id uuid NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
    match_id uuid NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    display_order integer NOT NULL DEFAULT 0 CHECK (display_order >= 0),
    PRIMARY KEY (media_asset_id, match_id)
);
CREATE TABLE media_player_links (
    media_asset_id uuid NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    display_order integer NOT NULL DEFAULT 0 CHECK (display_order >= 0),
    PRIMARY KEY (media_asset_id, player_id)
);

CREATE TABLE ledger_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
    account_type text NOT NULL CHECK (account_type IN ('user_cash', 'user_free_play', 'wager_escrow', 'house_clearing', 'migration_equity')),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    CHECK ((account_type IN ('user_cash', 'user_free_play') AND owner_user_id IS NOT NULL) OR
           (account_type NOT IN ('user_cash', 'user_free_play') AND owner_user_id IS NULL)),
    CHECK ((status = 'open' AND closed_at IS NULL) OR (status = 'closed' AND closed_at IS NOT NULL))
);
CREATE UNIQUE INDEX ledger_accounts_user_type_currency_unique
    ON ledger_accounts (owner_user_id, account_type, currency) WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX ledger_accounts_system_type_currency_unique
    ON ledger_accounts (account_type, currency) WHERE owner_user_id IS NULL;

CREATE TABLE ledger_transactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_type text NOT NULL CHECK (transaction_type IN ('wager_acceptance', 'wager_win', 'wager_loss', 'wager_refund', 'admin_adjustment', 'migration_adjustment', 'reversal')),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 200),
    source_type text NOT NULL CHECK (length(btrim(source_type)) BETWEEN 1 AND 80),
    source_id uuid,
    actor_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
    reason text,
    reversal_of_transaction_id uuid UNIQUE REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    expected_posting_count integer NOT NULL CHECK (expected_posting_count >= 2),
    correlation_id uuid,
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (currency, idempotency_key),
    CHECK (occurred_at <= created_at + interval '5 minutes'),
    CHECK (transaction_type <> 'reversal' OR reversal_of_transaction_id IS NOT NULL),
    CHECK (transaction_type NOT IN ('admin_adjustment', 'migration_adjustment', 'reversal') OR length(btrim(reason)) > 0)
);
CREATE INDEX ledger_transactions_source_idx ON ledger_transactions (source_type, source_id);
CREATE INDEX ledger_transactions_occurred_idx ON ledger_transactions (occurred_at, id);

CREATE TABLE ledger_postings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id uuid NOT NULL REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    account_id uuid NOT NULL REFERENCES ledger_accounts(id) ON DELETE RESTRICT,
    amount_cents bigint NOT NULL CHECK (amount_cents <> 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (transaction_id, account_id)
);
CREATE INDEX ledger_postings_account_history_idx ON ledger_postings (account_id, created_at, id);

CREATE OR REPLACE FUNCTION reject_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger history is immutable; post a reversal instead';
END;
$$;

CREATE TRIGGER ledger_transactions_immutable
BEFORE UPDATE OR DELETE ON ledger_transactions
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE TRIGGER ledger_postings_immutable
BEFORE UPDATE OR DELETE ON ledger_postings
FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE OR REPLACE FUNCTION check_ledger_transaction_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    checked_transaction_id uuid;
    posting_count integer;
    posting_total numeric;
    mismatched_accounts integer;
    expected_posting_count integer;
    transaction_type text;
    reversal_of_transaction_id uuid;
    reversal_mismatch_count integer;
BEGIN
    IF TG_TABLE_NAME = 'ledger_transactions' THEN
        checked_transaction_id := NEW.id;
    ELSE
        checked_transaction_id := NEW.transaction_id;
    END IF;

    SELECT count(p.id), COALESCE(sum(p.amount_cents), 0),
           t.expected_posting_count, t.transaction_type, t.reversal_of_transaction_id,
           count(*) FILTER (WHERE a.currency <> t.currency)
      INTO posting_count, posting_total, expected_posting_count, transaction_type,
           reversal_of_transaction_id, mismatched_accounts
      FROM ledger_transactions t
      LEFT JOIN ledger_postings p ON p.transaction_id = t.id
      LEFT JOIN ledger_accounts a ON a.id = p.account_id
     WHERE t.id = checked_transaction_id
     GROUP BY t.id, t.expected_posting_count, t.transaction_type, t.reversal_of_transaction_id;

    IF posting_count <> expected_posting_count THEN
        RAISE EXCEPTION 'ledger transaction % expected % postings but has %',
            checked_transaction_id, expected_posting_count, posting_count;
    END IF;
    IF posting_total <> 0 THEN
        RAISE EXCEPTION 'ledger transaction % is not balanced: % cents', checked_transaction_id, posting_total;
    END IF;
    IF mismatched_accounts <> 0 THEN
        RAISE EXCEPTION 'ledger transaction % contains an account with a different currency', checked_transaction_id;
    END IF;
    IF transaction_type = 'reversal' THEN
        IF EXISTS (
            SELECT 1
              FROM ledger_transactions reversal
              JOIN ledger_transactions original ON original.id = reversal.reversal_of_transaction_id
             WHERE reversal.id = checked_transaction_id
               AND reversal.currency <> original.currency
        ) THEN
            RAISE EXCEPTION 'ledger reversal % has a different currency from its original transaction', checked_transaction_id;
        END IF;

        SELECT count(*) INTO reversal_mismatch_count
          FROM (
              SELECT account_id
                FROM ledger_postings
               WHERE transaction_id IN (checked_transaction_id, reversal_of_transaction_id)
               GROUP BY account_id
              HAVING count(*) <> 2 OR sum(amount_cents) <> 0
          ) mismatches;
        IF reversal_mismatch_count <> 0 THEN
            RAISE EXCEPTION 'ledger reversal % is not an account-by-account inverse of transaction %',
                checked_transaction_id, reversal_of_transaction_id;
        END IF;
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER ledger_transaction_requires_balanced_postings
AFTER INSERT ON ledger_transactions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION check_ledger_transaction_balanced();

CREATE CONSTRAINT TRIGGER ledger_posting_preserves_balance
AFTER INSERT ON ledger_postings
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION check_ledger_transaction_balanced();

CREATE TABLE markets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    market_type text NOT NULL CHECK (market_type IN ('match', 'future', 'prop')),
    match_id uuid REFERENCES matches(id) ON DELETE RESTRICT,
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    description text,
    state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'open', 'closed', 'settlement_pending', 'settled', 'voided', 'cancelled')),
    opens_at timestamptz,
    closes_at timestamptz NOT NULL,
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((market_type = 'match' AND match_id IS NOT NULL) OR (market_type <> 'match' AND match_id IS NULL)),
    CHECK (opens_at IS NULL OR closes_at > opens_at),
    UNIQUE (id, market_type)
);
CREATE INDEX markets_browse_idx ON markets (state, closes_at, market_type);
CREATE INDEX markets_match_idx ON markets (match_id) WHERE match_id IS NOT NULL;

CREATE TABLE selections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id uuid NOT NULL REFERENCES markets(id) ON DELETE RESTRICT,
    selection_key text NOT NULL CHECK (selection_key ~ '^[a-z0-9]+(?:[._-][a-z0-9]+)*$'),
    display_terms text NOT NULL CHECK (length(btrim(display_terms)) BETWEEN 1 AND 500),
    offered_american_odds integer NOT NULL CHECK (offered_american_odds <= -100 OR offered_american_odds >= 100),
    semantic_result_key text,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (market_id, selection_key),
    UNIQUE (market_id, id)
);

CREATE TABLE market_restrictions (
    market_id uuid NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason text NOT NULL CHECK (length(btrim(reason)) > 0),
    restricted_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (market_id, user_id)
);

CREATE TABLE wagers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    market_id uuid NOT NULL,
    selection_id uuid NOT NULL,
    funding_account_type text NOT NULL CHECK (funding_account_type IN ('user_cash', 'user_free_play')),
    stake_cents bigint NOT NULL CHECK (stake_cents > 0),
    currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    accepted_american_odds integer NOT NULL CHECK (accepted_american_odds <= -100 OR accepted_american_odds >= 100),
    accepted_terms text NOT NULL CHECK (length(btrim(accepted_terms)) BETWEEN 1 AND 1000),
    potential_profit_cents bigint NOT NULL CHECK (potential_profit_cents >= 0),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'rejected', 'settled', 'voided')),
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 200),
    placed_at timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz,
    accepted_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    rejected_at timestamptz,
    rejected_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    rejection_reason text,
    acceptance_ledger_transaction_id uuid UNIQUE REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    FOREIGN KEY (market_id, selection_id) REFERENCES selections(market_id, id) ON DELETE RESTRICT,
    UNIQUE (user_id, idempotency_key),
    CHECK ((state IN ('accepted', 'settled', 'voided') AND accepted_at IS NOT NULL AND acceptance_ledger_transaction_id IS NOT NULL) OR
           (state NOT IN ('accepted', 'settled', 'voided'))),
    CHECK ((state = 'rejected' AND rejected_at IS NOT NULL AND rejected_by IS NOT NULL AND length(btrim(rejection_reason)) > 0) OR state <> 'rejected')
);
CREATE INDEX wagers_user_history_idx ON wagers (user_id, placed_at DESC);
CREATE INDEX wagers_market_state_idx ON wagers (market_id, state);

CREATE TABLE market_settlements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    market_id uuid NOT NULL REFERENCES markets(id) ON DELETE RESTRICT,
    version integer NOT NULL CHECK (version > 0),
    settlement_type text NOT NULL CHECK (settlement_type IN ('graded', 'voided')),
    verified_result_id uuid REFERENCES verified_results(id) ON DELETE RESTRICT,
    settled_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    reason text,
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 200),
    supersedes_settlement_id uuid UNIQUE,
    settled_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (market_id, version),
    UNIQUE (market_id, idempotency_key),
    UNIQUE (market_id, id),
    FOREIGN KEY (market_id, supersedes_settlement_id) REFERENCES market_settlements(market_id, id) ON DELETE RESTRICT,
    CHECK ((version = 1 AND supersedes_settlement_id IS NULL) OR (version > 1 AND supersedes_settlement_id IS NOT NULL)),
    CHECK (verified_result_id IS NOT NULL OR (settled_by IS NOT NULL AND length(btrim(reason)) > 0))
);

CREATE TABLE market_settlement_outcomes (
    market_settlement_id uuid NOT NULL REFERENCES market_settlements(id) ON DELETE RESTRICT,
    market_id uuid NOT NULL REFERENCES markets(id) ON DELETE RESTRICT,
    selection_id uuid NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('win', 'loss', 'push', 'void')),
    FOREIGN KEY (market_id, market_settlement_id) REFERENCES market_settlements(market_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (market_id, selection_id) REFERENCES selections(market_id, id) ON DELETE RESTRICT,
    PRIMARY KEY (market_settlement_id, selection_id)
);

CREATE TABLE wager_settlements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    wager_id uuid NOT NULL REFERENCES wagers(id) ON DELETE RESTRICT,
    market_settlement_id uuid NOT NULL REFERENCES market_settlements(id) ON DELETE RESTRICT,
    result text NOT NULL CHECK (result IN ('win', 'loss', 'push', 'void')),
    stake_cents bigint NOT NULL CHECK (stake_cents > 0),
    profit_cents bigint NOT NULL CHECK (profit_cents >= 0),
    returned_cents bigint NOT NULL CHECK (returned_cents >= 0),
    ledger_transaction_id uuid NOT NULL UNIQUE REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
    supersedes_wager_settlement_id uuid UNIQUE,
    settled_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (wager_id, market_settlement_id),
    UNIQUE (wager_id, id),
    FOREIGN KEY (wager_id, supersedes_wager_settlement_id) REFERENCES wager_settlements(wager_id, id) ON DELETE RESTRICT,
    CHECK ((result = 'win' AND returned_cents = stake_cents + profit_cents) OR
           (result = 'loss' AND returned_cents = 0 AND profit_cents = 0) OR
           (result IN ('push', 'void') AND returned_cents = stake_cents AND profit_cents = 0))
);

CREATE TABLE audit_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
    action text NOT NULL CHECK (length(btrim(action)) BETWEEN 1 AND 120),
    target_type text NOT NULL CHECK (length(btrim(target_type)) BETWEEN 1 AND 80),
    target_id uuid,
    reason text,
    request_id uuid,
    correlation_id uuid,
    before_data jsonb CHECK (before_data IS NULL OR jsonb_typeof(before_data) = 'object'),
    after_data jsonb CHECK (after_data IS NULL OR jsonb_typeof(after_data) = 'object'),
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_entries_target_idx ON audit_entries (target_type, target_id, occurred_at DESC);
CREATE INDEX audit_entries_actor_idx ON audit_entries (actor_user_id, occurred_at DESC);

CREATE OR REPLACE FUNCTION reject_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit history is immutable';
END;
$$;
CREATE TRIGGER audit_entries_immutable
BEFORE UPDATE OR DELETE ON audit_entries
FOR EACH ROW EXECUTE FUNCTION reject_audit_mutation();

CREATE TABLE outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type text NOT NULL CHECK (length(btrim(aggregate_type)) BETWEEN 1 AND 80),
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    event_type text NOT NULL CHECK (event_type ~ '^[A-Z][A-Za-z0-9]+\.v[1-9][0-9]*$'),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    occurred_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'processing', 'completed', 'failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL DEFAULT 10 CHECK (max_attempts BETWEEN 1 AND 100),
    locked_at timestamptz,
    locked_by text,
    completed_at timestamptz,
    failed_at timestamptz,
    last_error text,
    correlation_id uuid,
    causation_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (aggregate_type, aggregate_id, aggregate_version, event_type),
    CHECK (occurred_at <= created_at + interval '5 minutes'),
    CHECK ((state = 'pending' AND completed_at IS NULL AND failed_at IS NULL) OR state <> 'pending'),
    CHECK ((state = 'processing' AND locked_at IS NOT NULL AND locked_by IS NOT NULL) OR state <> 'processing'),
    CHECK ((state = 'completed' AND completed_at IS NOT NULL AND failed_at IS NULL) OR state <> 'completed'),
    CHECK ((state = 'failed' AND failed_at IS NOT NULL AND length(btrim(last_error)) > 0) OR state <> 'failed')
);
CREATE INDEX outbox_events_dispatch_idx ON outbox_events (available_at, occurred_at, id)
    WHERE state = 'pending';
CREATE INDEX outbox_events_stale_lock_idx ON outbox_events (locked_at)
    WHERE state = 'processing';

CREATE TABLE event_receipts (
    consumer text NOT NULL CHECK (length(btrim(consumer)) BETWEEN 1 AND 120),
    event_id uuid NOT NULL REFERENCES outbox_events(id) ON DELETE RESTRICT,
    processed_at timestamptz NOT NULL DEFAULT now(),
    result jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result) = 'object'),
    PRIMARY KEY (consumer, event_id)
);
