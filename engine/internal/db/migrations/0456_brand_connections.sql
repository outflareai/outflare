-- Brands own their data, step 6b of making brands fully separate
-- (plans/brands/README.md). Expand only (docs/migrations.md).
--
-- Most tables left in brandtables/pending.txt are a connection's machinery
-- (its syncs, hooks, limits, sign-ins, id maps) and move to workspace.txt:
-- connections are shared by the workspace, and what they bring in carries
-- its brand on the rows it lands in. The two below hold a brand's data and
-- take it from their parent, like migration 0432's.

DO $$
DECLARE
    -- parent, child, column
    m text[] := ARRAY[
        'infra_plans', 'infra_plan_items', 'plan_id',
        'records', 'news_article_authors', 'record_id'];
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

-- Partner and promo codes become unique per brand. These are the per-brand
-- twins of the per-workspace rules; this version adds codes without naming
-- the per-workspace ones, so the next migration can drop them and two
-- brands can then use the same code.
CREATE UNIQUE INDEX partner_memberships_brand_code_idx ON partner_memberships (brand_id, lower(code));
CREATE UNIQUE INDEX partner_promo_codes_brand_code_idx ON partner_promo_codes (brand_id, lower(code));
CREATE UNIQUE INDEX partner_clicks_brand_external_idx ON partner_clicks (brand_id, external_id) WHERE external_id IS NOT NULL;

-- A support conversation with no brand takes its mailbox's, else the
-- workspace's only brand. New ones do the same in code. What is left (mail
-- to a mailbox or address in no brand, in a workspace of several brands)
-- stays in every brand's inbox until its mailbox is put in a brand, so no
-- customer's mail goes unseen.
UPDATE support_conversations c SET brand_id = m.brand_id
FROM mailboxes m WHERE m.id = c.mailbox_id AND c.brand_id IS NULL AND m.brand_id IS NOT NULL;
UPDATE support_conversations SET brand_id = outflare_only_brand(workspace_id) WHERE brand_id IS NULL;
