-- A market carries the single currency all of its wagers are placed and
-- settled in. Existing staged markets (if any) predate multi-currency and are
-- Canadian dollars; the default is dropped immediately so every new market
-- must state its currency explicitly.
ALTER TABLE markets
    ADD COLUMN currency char(3) NOT NULL DEFAULT 'CAD' CHECK (currency ~ '^[A-Z]{3}$');

ALTER TABLE markets
    ALTER COLUMN currency DROP DEFAULT;

ALTER TABLE markets
    ADD CONSTRAINT markets_id_currency_unique UNIQUE (id, currency);

-- A wager's stored currency must always match its market's currency.
ALTER TABLE wagers
    ADD CONSTRAINT wagers_market_currency_fkey
    FOREIGN KEY (market_id, currency) REFERENCES markets (id, currency) ON DELETE RESTRICT;
