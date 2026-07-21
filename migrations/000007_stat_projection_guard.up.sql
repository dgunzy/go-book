-- Idempotency guard for the event-driven statistics projection. A match's
-- verified result updates each participant's player_stat_projections exactly
-- once: the projection consumer inserts the match_id here first and only
-- applies the increments when the insert is new, so redelivery or replay of
-- MatchResultVerified can never double-count a match.
CREATE TABLE match_stat_applications (
    match_id uuid PRIMARY KEY REFERENCES matches(id) ON DELETE CASCADE,
    applied_at timestamptz NOT NULL DEFAULT now()
);
