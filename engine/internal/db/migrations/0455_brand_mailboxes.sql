-- Brands own their data, step 6 of making brands fully separate
-- (plans/brands/README.md): every mailbox and sending domain belongs to
-- one brand, the owner's decision of 2026-11-04. A mailbox with no brand
-- is unassigned (no brand, no data): it serves no brand's sequences until
-- someone puts it in one, and the workspace shows it as unassigned. Expand
-- only (docs/migrations.md): brand_id stays nullable.
--
-- The previous version reads a mailbox with no brand as shared by every
-- brand. Giving those mailboxes a brand only narrows which sequences they
-- may serve to the ones they serve now, so it runs the same on this schema.

-- A mailbox with no brand takes the brand of the sequence it serves, else
-- its sending domain's, else the workspace's only brand.
UPDATE mailboxes m SET brand_id = q.brand_id
FROM mailbox_assignments x JOIN sequences q ON q.id = x.sequence_id
WHERE x.mailbox_id = m.id AND m.brand_id IS NULL AND q.brand_id IS NOT NULL;

UPDATE mailboxes m SET brand_id = d.brand_id
FROM sending_domains d
WHERE d.workspace_id = m.workspace_id AND d.domain = m.domain AND m.brand_id IS NULL AND d.brand_id IS NOT NULL;

CREATE FUNCTION outflare_only_brand(ws uuid) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN count(*) = 1 THEN (array_agg(id))[1] END
    FROM brands WHERE workspace_id = ws AND status <> 'archived'
$$;

UPDATE mailboxes SET brand_id = outflare_only_brand(workspace_id) WHERE brand_id IS NULL;

-- A sending domain with no brand takes its mailboxes' brand when they
-- share one, else the workspace's only brand.
UPDATE sending_domains d SET brand_id = b.brand_id
FROM (SELECT workspace_id, domain, (array_agg(DISTINCT brand_id))[1] AS brand_id
      FROM mailboxes WHERE brand_id IS NOT NULL
      GROUP BY workspace_id, domain HAVING count(DISTINCT brand_id) = 1) b
WHERE b.workspace_id = d.workspace_id AND b.domain = d.domain AND d.brand_id IS NULL;

UPDATE sending_domains SET brand_id = outflare_only_brand(workspace_id) WHERE brand_id IS NULL;

-- The brand a new mailbox takes when none is given: its connection's, its
-- domain's, or the workspace's only brand; otherwise none (unassigned).
-- This version's code calls it when it adds a mailbox; it isn't a trigger,
-- so the previous version adds mailboxes as it always did.
CREATE FUNCTION outflare_mailbox_brand(ws uuid, email text, integration uuid) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT brand_id FROM integrations WHERE id = integration AND workspace_id = ws),
        (SELECT brand_id FROM sending_domains WHERE workspace_id = ws AND domain = lower(split_part(email, '@', 2))),
        outflare_only_brand(ws))
$$;

-- The rows under a mailbox or a sequence carry its brand
-- (outflare_parent_brand and outflare_children_moved, migration 0432).
DO $$
DECLARE
    -- parent, child, column
    m text[] := ARRAY[
        'mailboxes', 'mailbox_assignments', 'mailbox_id',
        'mailboxes', 'mailbox_locks', 'mailbox_id',
        'mailboxes', 'mailbox_platforms', 'mailbox_id',
        'mailboxes', 'allocation_changes', 'mailbox_id',
        'sequences', 'allocation_declines', 'sequence_id',
        'sequences', 'allocation_suggestions', 'sequence_id',
        'sequences', 'allocation_status', 'sequence_id',
        'sequences', 'link_sequence_settings', 'sequence_id'];
    parent text;
    child text;
    col text;
    args text;
    i int;
BEGIN
    FOR i IN 0 .. array_length(m, 1) / 3 - 1 LOOP
        parent := m[3 * i + 1];
        child := m[3 * i + 2];
        col := m[3 * i + 3];
        EXECUTE format('ALTER TABLE %I ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL', child);
        EXECUTE format('UPDATE %I x SET brand_id = p.brand_id FROM %I p WHERE p.id = x.%I', child, parent, col);
        EXECUTE format('CREATE INDEX %I ON %I (brand_id) WHERE brand_id IS NOT NULL', child || '_brand_idx', child);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION outflare_parent_brand(%L, %L)',
            child || '_brand', child, parent, col);
    END LOOP;
    FOR parent IN SELECT DISTINCT x FROM unnest(m) WITH ORDINALITY AS u(x, n) WHERE n % 3 = 1 LOOP
        SELECT string_agg(format('%L, %L', m[3 * j + 2], m[3 * j + 3]), ', ') INTO args
            FROM generate_series(0, array_length(m, 1) / 3 - 1) j WHERE m[3 * j + 1] = parent;
        EXECUTE format('CREATE TRIGGER %I AFTER UPDATE OF brand_id ON %I FOR EACH ROW WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id)
            EXECUTE FUNCTION outflare_children_moved(%s)', parent || '_rows_moved', parent, args);
    END LOOP;
END
$$;
