DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM legacy_book_user_mappings WHERE import_state = 'promoted') THEN
        RAISE EXCEPTION 'cannot roll back identity and legacy book migration after financial promotion';
    END IF;
END;
$$;

DROP VIEW IF EXISTS ledger_account_balances;
DROP TRIGGER IF EXISTS legacy_book_user_mappings_immutable ON legacy_book_user_mappings;
DROP TRIGGER IF EXISTS legacy_book_wagers_immutable ON legacy_book_wagers;
DROP TRIGGER IF EXISTS legacy_book_transactions_immutable ON legacy_book_transactions;
DROP FUNCTION IF EXISTS reject_legacy_book_history_mutation();
DROP TABLE IF EXISTS legacy_book_wagers;
DROP TABLE IF EXISTS legacy_book_transactions;
DROP TABLE IF EXISTS legacy_book_user_mappings;
DROP TABLE IF EXISTS oidc_login_attempts;
