-- Link orders catalog: a manual "Best link" label on a donor, set by a
-- person for the sites the brand most wants links from. The catalog and
-- the recommended donors show them first. Like the PBN flag it is never
-- set automatically.
--
-- Has a default, so the previous release keeps writing rows without it.
ALTER TABLE coverage_donor_profiles
    ADD COLUMN best_link boolean NOT NULL DEFAULT false;
