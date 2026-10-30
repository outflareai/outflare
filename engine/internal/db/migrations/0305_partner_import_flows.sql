-- Imports also bring a platform's groups and flows, matched by their ids
-- like everything else on a later run.
ALTER TABLE partner_import_refs DROP CONSTRAINT partner_import_refs_kind_check;
ALTER TABLE partner_import_refs ADD CONSTRAINT partner_import_refs_kind_check
    CHECK (kind IN ('partner', 'customer', 'commission', 'payout', 'promo_code', 'group', 'flow'));
