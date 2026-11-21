-- An agent can cancel its own change while it still waits for a person
-- (status cancelled, which the table already takes), and say which later
-- change replaces it, so a person deciding sees the newer one and never
-- approves a stale request by mistake.
--
-- Expand only (docs/migrations.md): one nullable column the previous
-- version never reads.
ALTER TABLE actions ADD COLUMN superseded_by uuid REFERENCES actions(id) ON DELETE SET NULL;
