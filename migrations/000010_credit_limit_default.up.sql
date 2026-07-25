-- Raise the standard credit line from $1,000 to $1,500.
--
-- The authoritative default now lives in internal/config
-- (DEFAULT_CREDIT_LIMIT_CENTS, default 150000): application code passes it
-- explicitly when an invited member is created. This column DEFAULT is kept in
-- sync as a fallback for direct SQL inserts (bootstrap-owner, mock-seed,
-- integration fixtures) and must be changed together with the config default.
ALTER TABLE users ALTER COLUMN credit_limit_cents SET DEFAULT 150000;

-- Members still sitting on the previous $1,000 default move up with it.
-- Anyone whose limit was set deliberately to some other amount is left alone:
-- an operator's per-player decision outranks a change of default.
UPDATE users SET credit_limit_cents = 150000, updated_at = now()
WHERE credit_limit_cents = 100000;
