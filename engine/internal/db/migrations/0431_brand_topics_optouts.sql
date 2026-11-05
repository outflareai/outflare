-- Brands own their data, step 4 of making brands fully separate
-- (plans/brands/README.md): email topics carry brand_id; an opt-out gets
-- a per-brand unique rule; and partner groups and reward rules carry
-- their program's brand (the rest of the partner tables did in 0430).
-- Expand only (docs/migrations.md): the per-workspace unique rules these
-- replace go in the contract migration, once no serving version upserts
-- against them.

-- Email topics: what was every brand's goes to the workspace's first
-- brand. The version before this one reads a topic's brand through
-- to_jsonb, so this column changes nothing it shows. A topic it makes
-- has no brand (every brand's, as it shows it) until the contract step
-- gives those the first brand: it doesn't say which brand made it.
ALTER TABLE email_topics ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE email_topics SET brand_id = outflare_first_brand(workspace_id);
CREATE UNIQUE INDEX email_topics_brand_key_idx ON email_topics (brand_id, key);

-- One opt-out per brand and address (null: every brand's, such as a hard
-- bounce). Next to the per-workspace rule until the contract step.
CREATE UNIQUE INDEX suppressions_brand_kind_value_idx ON suppressions (workspace_id, brand_id, kind, value) NULLS NOT DISTINCT;

-- Partner groups and reward rules.
ALTER TABLE partner_groups ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE partner_reward_rules ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
CREATE TRIGGER partner_groups_brand BEFORE INSERT ON partner_groups FOR EACH ROW EXECUTE FUNCTION outflare_partner_brand();
CREATE TRIGGER partner_reward_rules_brand BEFORE INSERT ON partner_reward_rules FOR EACH ROW EXECUTE FUNCTION outflare_partner_brand();
UPDATE partner_groups x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id;
UPDATE partner_reward_rules x SET brand_id = p.brand_id FROM partner_programs p WHERE p.id = x.program_id;
CREATE INDEX partner_groups_brand_idx ON partner_groups (brand_id);
CREATE INDEX partner_reward_rules_brand_idx ON partner_reward_rules (brand_id);

-- Moving a program to another brand moves its groups and rules too.
CREATE OR REPLACE FUNCTION outflare_partner_program_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'partner_groups', 'partner_reward_rules',
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
