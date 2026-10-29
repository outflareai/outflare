-- The group new partners join, like Tolt's default group. Null puts them in
-- no group.
ALTER TABLE partner_programs
    ADD COLUMN default_group_id uuid REFERENCES partner_groups (id) ON DELETE SET NULL;
