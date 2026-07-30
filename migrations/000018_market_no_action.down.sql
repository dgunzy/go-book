-- Any market closed out without action returns to plain closed, which is the
-- state it was in before, so the constraint can be narrowed again.
UPDATE markets SET state = 'closed', updated_at = now() WHERE state = 'closed_no_action';

ALTER TABLE markets DROP CONSTRAINT markets_state_check;
ALTER TABLE markets ADD CONSTRAINT markets_state_check
    CHECK (state IN ('draft', 'open', 'closed', 'settlement_pending', 'settled',
                     'voided', 'cancelled'));
