-- Brands own their data, step 5 of making brands fully separate
-- (plans/brands/README.md): the rows under a brand's SEO sites, AI
-- visibility cohorts, knowledge, help centres, support conversations,
-- inbound addresses, missions, link building, stories, news searches,
-- social accounts and posts and Reddit actions carry their parent's brand,
-- so a brand's data is WHERE brand_id = $1 all the way down. Expand only
-- (docs/migrations.md): reads still go through the parent.
--
-- A row takes its parent's brand by trigger (none when its parent has
-- none; the first brand when it has no parent), and moving a parent to
-- another brand moves the rows under it, and theirs in turn.

CREATE FUNCTION outflare_parent_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    ref text;
    b uuid;
BEGIN
    IF NEW.brand_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- TG_ARGV: the parent table, then this table's column that points at it.
    ref := to_jsonb(NEW) ->> TG_ARGV[1];
    IF ref IS NULL THEN
        NEW.brand_id := outflare_first_brand(NEW.workspace_id);
    ELSE
        EXECUTE format('SELECT brand_id FROM %I WHERE id = $1::uuid', TG_ARGV[0]) INTO b USING ref;
        NEW.brand_id := b;
    END IF;
    RETURN NEW;
END
$$;

CREATE FUNCTION outflare_children_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    i int;
BEGIN
    -- TG_ARGV: pairs of a child table and its column that points here.
    FOR i IN 0 .. TG_NARGS / 2 - 1 LOOP
        EXECUTE format('UPDATE %I SET brand_id = $1 WHERE %I = $2 AND brand_id IS DISTINCT FROM $1', TG_ARGV[2 * i], TG_ARGV[2 * i + 1])
            USING NEW.brand_id, NEW.id;
    END LOOP;
    RETURN NEW;
END
$$;

DO $$
DECLARE
    -- parent, child, column; parents before their children's children.
    m text[] := ARRAY[
        'seo_sites', 'backlink_runs', 'site_id',
        'seo_sites', 'backlink_events', 'site_id',
        'seo_sites', 'competitor_events', 'site_id',
        'seo_sites', 'crawl_runs', 'site_id',
        'seo_sites', 'fix_autonomy', 'site_id',
        'seo_sites', 'indexability_snapshots', 'site_id',
        'seo_sites', 'page_targets', 'site_id',
        'seo_sites', 'seo_regressions', 'site_id',
        'seo_sites', 'seo_site_read_failures', 'site_id',
        'seo_sites', 'tracked_keywords', 'site_id',
        'seo_competitors', 'competitor_pages', 'competitor_id',
        'crawl_runs', 'crawl_pages', 'run_id',
        'crawl_runs', 'crawl_issues', 'run_id',
        'crawl_runs', 'crawl_links', 'run_id',
        'crawl_runs', 'crawl_ai_bots', 'run_id',
        'tracked_keywords', 'rank_observations', 'tracked_id',
        'ai_cohorts', 'ai_prompts', 'cohort_id',
        'ai_cohorts', 'ai_runs', 'cohort_id',
        'ai_samples', 'ai_citations', 'sample_id',
        'ai_samples', 'ai_mentions', 'sample_id',
        'ai_samples', 'ai_fact_checks', 'sample_id',
        'knowledge_items', 'knowledge_chunks', 'item_id',
        'knowledge_items', 'knowledge_uses', 'item_id',
        'knowledge_items', 'knowledge_versions', 'item_id',
        'knowledge_items', 'help_article_days', 'item_id',
        'help_centres', 'help_centre_days', 'centre_id',
        'support_conversations', 'support_messages', 'conversation_id',
        'inbound_addresses', 'inbound_emails', 'address_id',
        'missions', 'mission_actions', 'mission_id',
        'missions', 'mission_calls', 'mission_id',
        'missions', 'mission_updates', 'mission_id',
        'link_prospects', 'link_page_checks', 'prospect_id',
        'link_prospects', 'link_prospect_contacts', 'prospect_id',
        'link_prospects', 'link_pitches', 'prospect_id',
        'link_reads', 'link_read_rows', 'read_id',
        'stories', 'story_facts', 'story_id',
        'stories', 'story_pitches', 'story_id',
        'stories', 'story_quotes', 'story_id',
        'news_searches', 'news_hits', 'search_id',
        'social_accounts', 'social_account_days', 'account_id',
        'social_posts', 'social_post_snapshots', 'post_id',
        'reddit_actions', 'reddit_action_events', 'action_id',
        'reddit_actions', 'reddit_action_snapshots', 'action_id'];
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
        EXECUTE format('UPDATE %I SET brand_id = outflare_first_brand(workspace_id) WHERE %I IS NULL', child, col);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION outflare_parent_brand(%L, %L)',
            child || '_brand', child, parent, col);
    END LOOP;
    -- One trigger per parent, naming all its children.
    FOR parent IN SELECT DISTINCT x FROM unnest(m) WITH ORDINALITY AS u(x, n) WHERE n % 3 = 1 LOOP
        SELECT string_agg(format('%L, %L', m[3 * j + 2], m[3 * j + 3]), ', ') INTO args
            FROM generate_series(0, array_length(m, 1) / 3 - 1) j WHERE m[3 * j + 1] = parent;
        EXECUTE format('CREATE TRIGGER %I AFTER UPDATE OF brand_id ON %I FOR EACH ROW WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id)
            EXECUTE FUNCTION outflare_children_moved(%s)', parent || '_children_moved', parent, args);
    END LOOP;
END
$$;
