-- A market that closed with nobody on it has nothing to grade. Working out the
-- winner of a five-way prop that carried no money is busywork, and recording it
-- as a settlement implies money moved when none did.
--
-- This is deliberately not 'voided' and not 'cancelled'. Void means the market
-- was live with stakes on it and is being unwound with refunds; cancelled means
-- it never really ran. A market that opened, took no interest and closed is a
-- third thing, and reconciliation should be able to tell them apart.
ALTER TABLE markets DROP CONSTRAINT markets_state_check;
ALTER TABLE markets ADD CONSTRAINT markets_state_check
    CHECK (state IN ('draft', 'open', 'closed', 'settlement_pending', 'settled',
                     'voided', 'cancelled', 'closed_no_action'));
