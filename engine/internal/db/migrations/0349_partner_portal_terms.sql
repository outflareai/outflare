-- Keep explicit acceptance when a program changes its terms address or revision.
-- Existing partners review the current terms at their next portal visit.
ALTER TABLE partner_memberships ADD COLUMN accepted_terms_version text NOT NULL DEFAULT '';
