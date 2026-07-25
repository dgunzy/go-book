-- Record who placed a wager when it was not the member themselves.
--
-- An admin can now place a bet on a member's behalf. The wager still belongs
-- to that member — it is their stake, their balance, their result — but the
-- row has to say who put it on, or a later "I never placed that" has no
-- answer. NULL means the member placed it themselves, which is every wager
-- taken before this.
ALTER TABLE wagers
    ADD COLUMN placed_by uuid REFERENCES users(id) ON DELETE RESTRICT;

CREATE INDEX wagers_placed_by_idx ON wagers (placed_by) WHERE placed_by IS NOT NULL;
