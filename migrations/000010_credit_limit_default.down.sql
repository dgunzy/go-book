-- Restore the previous $1,000 standard credit line. The UPDATE mirrors the
-- forward migration: only members left on the $1,500 default move back, so a
-- deliberately set per-player limit survives the rollback.
UPDATE users SET credit_limit_cents = 100000, updated_at = now()
WHERE credit_limit_cents = 150000;

ALTER TABLE users ALTER COLUMN credit_limit_cents SET DEFAULT 100000;
