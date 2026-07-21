-- Per-player credit limit. A member's cash balance may go negative (they owe
-- the book) down to -credit_limit_cents. The default gives everyone a $1,000
-- credit line; raise it per player when they "apply for extra".
ALTER TABLE users
    ADD COLUMN credit_limit_cents bigint NOT NULL DEFAULT 100000
    CHECK (credit_limit_cents >= 0);
