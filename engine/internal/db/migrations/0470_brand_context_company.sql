-- Brands are fully separate (plans/brands/README.md): "About your company"
-- is each brand's own, and nothing is shared between brands. Until now a
-- workspace had one company context (workspace_context) that every brand
-- read under its own brand context. Each brand now starts with a copy of
-- it, merged with what the brand already had:
--
-- - about, goals and voice: the brand's where it set one, else the
--   company's (the brand's already won where set);
-- - rules and notes: the company's, then the brand's (rules always added
--   to the company's), with how the brands relate kept in the notes;
-- - website, logo and colours: the brand's where set, else the company's;
-- - whether a person stopped Outflare asking for the missing fields.
--
-- From this version nothing reads workspace_context; a later release
-- drops it.
ALTER TABLE brand_context ADD COLUMN onboarding_dismissed_at timestamptz;

INSERT INTO brand_context (brand_id, workspace_id)
SELECT id, workspace_id FROM brands
ON CONFLICT (brand_id) DO NOTHING;

UPDATE brand_context bc SET
    about = COALESCE(NULLIF(bc.about, ''), wc.about),
    goals = COALESCE(NULLIF(bc.goals, ''), wc.goals),
    voice = COALESCE(NULLIF(bc.voice, ''), wc.voice),
    rules = concat_ws(E'\n\n', NULLIF(wc.rules, ''), NULLIF(bc.rules, '')),
    notes = concat_ws(E'\n\n', NULLIF(wc.notes, ''),
        CASE WHEN wc.brands <> '' THEN E'How our brands relate:\n' || wc.brands END,
        NULLIF(bc.notes, '')),
    onboarding_dismissed_at = COALESCE(bc.onboarding_dismissed_at, wc.onboarding_dismissed_at),
    version = bc.version + 1,
    updated_at = now()
FROM workspace_context wc
WHERE wc.workspace_id = bc.workspace_id
  AND (wc.about <> '' OR wc.goals <> '' OR wc.voice <> '' OR wc.rules <> '' OR wc.notes <> ''
       OR wc.brands <> '' OR wc.onboarding_dismissed_at IS NOT NULL);

UPDATE brands b SET
    website = COALESCE(NULLIF(b.website, ''), wc.website),
    logo_url = COALESCE(NULLIF(b.logo_url, ''), wc.logo_url),
    colors = CASE WHEN b.colors = '{}'::jsonb THEN wc.colors ELSE b.colors END
FROM workspace_context wc
WHERE wc.workspace_id = b.workspace_id
  AND ((b.website = '' AND wc.website <> '') OR (b.logo_url = '' AND wc.logo_url <> '')
       OR (b.colors = '{}'::jsonb AND wc.colors <> '{}'::jsonb));
