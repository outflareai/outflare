-- Brands are fully separate (plans/brands/README.md): who a brand's
-- mailboxes send as is the brand's own, the owner's decision of
-- 2026-11-05. Each brand starts with a copy of the workspace's senders.
--
-- A sender keeps its id in the brand most of its mailboxes send for (the
-- workspace's first brand when it has none); every other brand gets a
-- copy with its teams, and that brand's mailboxes and sequences move to
-- the copy.
--
-- Expand only (docs/migrations.md): brand_id stays nullable. The name was
-- unique in the workspace and is now unique in the brand. The previous
-- version adds senders without naming that key (it reports a clash from
-- the error), so on this schema it only stops refusing a name another
-- brand's sender has.
ALTER TABLE senders ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE CASCADE;

DROP INDEX senders_name_idx;
CREATE UNIQUE INDEX senders_brand_name_idx ON senders (brand_id, lower(name));
CREATE INDEX senders_ws_brand_idx ON senders (workspace_id, brand_id);

UPDATE senders sn SET brand_id = COALESCE(
    (SELECT m.brand_id FROM mailboxes m
     WHERE m.sender_id = sn.id AND m.brand_id IS NOT NULL
     GROUP BY m.brand_id ORDER BY count(*) DESC, m.brand_id LIMIT 1),
    outflare_first_brand(sn.workspace_id))
WHERE sn.brand_id IS NULL;

CREATE TEMP TABLE sender_copies ON COMMIT DROP AS
SELECT sn.id AS old_id, b.id AS brand_id, gen_random_uuid() AS new_id
FROM senders sn JOIN brands b ON b.workspace_id = sn.workspace_id AND b.id <> sn.brand_id;

INSERT INTO senders (id, workspace_id, brand_id, name, kind, user_id, status, created_at, updated_at)
SELECT c.new_id, sn.workspace_id, c.brand_id, sn.name, sn.kind, sn.user_id, sn.status, sn.created_at, sn.updated_at
FROM sender_copies c JOIN senders sn ON sn.id = c.old_id;

INSERT INTO sender_teams (sender_id, team_id)
SELECT c.new_id, t.team_id FROM sender_copies c JOIN sender_teams t ON t.sender_id = c.old_id;

UPDATE mailboxes m SET sender_id = c.new_id
FROM sender_copies c WHERE m.sender_id = c.old_id AND m.brand_id = c.brand_id;

UPDATE sequence_senders x SET sender_id = c.new_id
FROM sender_copies c, sequences q
WHERE x.sender_id = c.old_id AND q.id = x.sequence_id AND q.brand_id = c.brand_id;
