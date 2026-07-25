-- Record who moved a line by hand.
--
-- selection_price_changes was written only by the automatic pricing engine, so
-- every row was explained by its trigger_wager_id. An admin can now set a
-- selection's opening line mid-market, and that move needs the same audit as
-- any other admin action: who did it and why. Both columns are nullable
-- because automatic moves have neither.
ALTER TABLE selection_price_changes
    ADD COLUMN actor_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
    ADD COLUMN reason text;

-- A hand-set line is exactly the row that carries an actor, so it must carry
-- the reason too; automatic moves carry neither.
ALTER TABLE selection_price_changes
    ADD CONSTRAINT selection_price_changes_manual_reason_check
    CHECK ((actor_user_id IS NULL AND reason IS NULL)
        OR (actor_user_id IS NOT NULL AND length(btrim(reason)) > 0));
