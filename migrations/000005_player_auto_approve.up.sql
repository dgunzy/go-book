-- Per-player auto-approve override. NULL means the user falls back to the
-- book-wide WAGER_AUTO_APPROVE_MAX_CENTS default; a value is that user's own
-- largest auto-approved stake in cents (0 forces every wager to manual review).
ALTER TABLE users
    ADD COLUMN wager_auto_approve_max_cents bigint
    CHECK (wager_auto_approve_max_cents IS NULL OR wager_auto_approve_max_cents >= 0);
