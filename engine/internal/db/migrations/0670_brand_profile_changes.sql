-- The history of a brand's profile: every time a fact or a persona is
-- added, changed, confirmed, retired or brought back, with who did it and
-- what it said before and after. Rows are only ever added.
CREATE TABLE brand_profile_changes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    fact_id      uuid REFERENCES brand_profile_facts (id) ON DELETE CASCADE,
    persona_id   uuid REFERENCES brand_personas (id) ON DELETE CASCADE,
    action       text NOT NULL CHECK (action IN ('created', 'updated', 'confirmed', 'retired', 'restored', 'imported')),
    -- Each row is about one fact or one persona. The fields that changed,
    -- as they were and as they are now.
    before       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(before) = 'object'),
    after        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(after) = 'object'),
    actor_id     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (fact_id IS NOT NULL OR persona_id IS NOT NULL)
);

CREATE INDEX brand_profile_changes_brand_idx ON brand_profile_changes (brand_id, created_at DESC);
CREATE INDEX brand_profile_changes_fact_idx ON brand_profile_changes (fact_id, created_at DESC) WHERE fact_id IS NOT NULL;
CREATE INDEX brand_profile_changes_persona_idx ON brand_profile_changes (persona_id, created_at DESC) WHERE persona_id IS NOT NULL;

-- Each brand's older context fields start the profile: one confirmed fact
-- per field that has text, which then changes together with the field.
INSERT INTO brand_profile_facts (workspace_id, brand_id, section, title, text, source, visibility, status,
                                 context_field, confirmed_by, confirmed_at, created_by, updated_by)
SELECT c.workspace_id, c.brand_id, f.section, f.title, f.text, 'context', f.visibility, 'confirmed',
       f.field, c.updated_by, c.updated_at, c.updated_by, c.updated_by
FROM brand_context c
CROSS JOIN LATERAL (VALUES
    ('about', 'company', 'What the brand is', trim(c.about), 'customers'),
    ('audience', 'customers', 'Who it is for', trim(c.audience), 'customers'),
    ('goals', 'goals', 'Goals', trim(c.goals), 'internal'),
    ('voice', 'voice', 'Voice', trim(c.voice), 'customers'),
    ('rules', 'rules', 'Rules', trim(c.rules), 'internal')
) AS f (field, section, title, text, visibility)
WHERE f.text <> '' AND length(f.text) <= 10000;

INSERT INTO brand_profile_changes (workspace_id, brand_id, fact_id, action, after, actor_id)
SELECT workspace_id, brand_id, id, 'imported', jsonb_build_object('text', text, 'section', section), created_by
FROM brand_profile_facts WHERE context_field IS NOT NULL;
