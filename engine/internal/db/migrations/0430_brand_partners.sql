-- Brands own their data, step 3 of making brands fully separate
-- (plans/brands/README.md): partner programs and everything under them
-- (partners, promo codes, clicks, referrals, conversions, rewards,
-- payouts, postbacks, portal sign-ins, assets and imports) carry brand_id
-- on the row itself, so a brand's affiliate data is exactly
-- WHERE brand_id = $1. Expand only (docs/migrations.md).
--
-- Partner groups and reward rules follow in the next migration: the
-- version before this one can't seed them in its brand-leak test, so a
-- brand column on them now would fail that version's tests here.
--
-- A program shared by every brand goes to the workspace's first brand:
-- a brand's partners, their links and their money are never another
-- brand's. Rows take their program's brand by trigger, and moving a
-- program to another brand moves every row under it.

UPDATE partner_programs SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

CREATE FUNCTION outflare_partner_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    b uuid;
BEGIN
    IF NEW.brand_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- One branch per table: plpgsql only resolves the one it runs.
    IF TG_TABLE_NAME IN ('partner_promo_codes', 'partner_clicks', 'partner_payouts', 'partner_postback_sends') THEN
        SELECT p.brand_id INTO b FROM partner_memberships m JOIN partner_programs p ON p.id = m.program_id WHERE m.id = NEW.membership_id;
    ELSE
        SELECT brand_id INTO b FROM partner_programs WHERE id = NEW.program_id;
    END IF;
    NEW.brand_id := COALESCE(b, outflare_first_brand(NEW.workspace_id));
    RETURN NEW;
END
$$;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'partner_memberships', 'partner_promo_codes', 'partner_clicks',
        'partner_referrals', 'partner_conversions', 'partner_payouts', 'partner_rewards', 'partner_sign_in_links',
        'partner_sessions', 'partner_assets', 'partner_imports', 'partner_import_refs', 'partner_postback_sends']
    LOOP
        EXECUTE format('ALTER TABLE %I ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION outflare_partner_brand()', t || '_brand', t);
        IF t IN ('partner_promo_codes', 'partner_clicks', 'partner_payouts', 'partner_postback_sends') THEN
            EXECUTE format('UPDATE %I x SET brand_id = p.brand_id FROM partner_memberships m JOIN partner_programs p ON p.id = m.program_id WHERE m.id = x.membership_id', t);
        ELSE
            EXECUTE format('UPDATE %I x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id', t);
        END IF;
        EXECUTE format('CREATE INDEX %I ON %I (brand_id)', t || '_brand_idx', t);
    END LOOP;
END
$$;

-- Rows that already had a brand column follow their program too.
UPDATE partner_tasks x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id AND x.brand_id IS DISTINCT FROM p.brand_id;
UPDATE partner_audiences x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id AND x.brand_id IS DISTINCT FROM p.brand_id;
UPDATE partner_task_submissions x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id AND x.brand_id IS DISTINCT FROM p.brand_id;
UPDATE partner_proposals x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id AND x.brand_id IS DISTINCT FROM p.brand_id;
UPDATE partner_lead_submissions x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id AND x.brand_id IS DISTINCT FROM p.brand_id;
UPDATE partner_conversion_log x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id AND x.brand_id IS DISTINCT FROM p.brand_id;

-- Moving a program to another brand moves everything under it.
CREATE FUNCTION outflare_partner_program_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'partner_memberships', 'partner_referrals', 'partner_conversions',
        'partner_rewards', 'partner_sign_in_links', 'partner_sessions', 'partner_assets', 'partner_imports', 'partner_import_refs',
        'partner_tasks', 'partner_audiences', 'partner_task_submissions', 'partner_proposals', 'partner_lead_submissions', 'partner_conversion_log']
    LOOP
        EXECUTE format('UPDATE %I SET brand_id = $1 WHERE program_id = $2', t) USING NEW.brand_id, NEW.id;
    END LOOP;
    FOREACH t IN ARRAY ARRAY['partner_promo_codes', 'partner_clicks', 'partner_payouts', 'partner_postback_sends']
    LOOP
        EXECUTE format('UPDATE %I x SET brand_id = $1 FROM partner_memberships m WHERE m.id = x.membership_id AND m.program_id = $2', t) USING NEW.brand_id, NEW.id;
    END LOOP;
    RETURN NEW;
END
$$;

CREATE TRIGGER partner_programs_moved AFTER UPDATE OF brand_id ON partner_programs
    FOR EACH ROW WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id) EXECUTE FUNCTION outflare_partner_program_moved();

-- Program slugs per brand; the per-workspace rule goes in a contract step.
CREATE UNIQUE INDEX partner_programs_brand_slug_idx ON partner_programs (brand_id, slug);
