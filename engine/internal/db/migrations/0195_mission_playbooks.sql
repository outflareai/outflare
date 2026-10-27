-- Playbooks: a mission started from a playbook in the repository's
-- playbooks/ folder remembers which one and which version, so results can
-- be compared per playbook. Playbooks themselves are files, not rows.
ALTER TABLE missions ADD COLUMN playbook_id text;
ALTER TABLE missions ADD COLUMN playbook_version integer;

CREATE INDEX missions_playbook ON missions (workspace_id, playbook_id) WHERE playbook_id IS NOT NULL;
