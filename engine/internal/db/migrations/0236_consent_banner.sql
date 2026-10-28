-- The consent banner in the website script (site.js): one per site key,
-- with the laws and frameworks it follows, the categories it asks about,
-- how it looks and what it says. A key without a row shows no banner and
-- keeps the script's data-consent behaviour.
CREATE TABLE consent_banners (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_key_id     uuid NOT NULL REFERENCES site_keys (id) ON DELETE CASCADE,
    -- The banner's settings (engine/internal/api/consent_banner.go,
    -- ConsentBanner in api/openapi.yaml). Fields left out take the
    -- defaults, so a new law or framework is on for every banner.
    config          jsonb NOT NULL DEFAULT '{}',
    -- Goes up on every save. Each choice records the version it was made
    -- on, as proof of what the visitor was shown.
    version         integer NOT NULL DEFAULT 1,
    -- Goes up when visitors must be asked again: a new category, a new
    -- purpose, or a person asking for it. A choice made on an older one is
    -- asked again where the law needs it.
    consent_version integer NOT NULL DEFAULT 1,
    updated_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX consent_banners_key_idx ON consent_banners (site_key_id);
CREATE INDEX consent_banners_workspace_idx ON consent_banners (workspace_id);

-- A visitor's choice on the banner is a consent before anyone knows who
-- they are: it is kept on their anonymous id, and identify gives it to
-- their record (backfill), as with their events and visits. Channel web
-- holds the banner's categories (analytics, marketing, preferences); the
-- ads channel holds Google Consent Mode's ad purposes, which ad send-back
-- reads.
ALTER TABLE consents ALTER COLUMN record_id DROP NOT NULL;
ALTER TABLE consents ADD COLUMN anonymous_id text;
ALTER TABLE consents ADD CONSTRAINT consents_subject_check CHECK (record_id IS NOT NULL OR anonymous_id IS NOT NULL);
CREATE INDEX consents_anonymous_idx ON consents (workspace_id, anonymous_id, occurred_at DESC) WHERE anonymous_id IS NOT NULL;
-- migration-safety: the new check only adds a value; every existing row and the previous version's writes still pass it.
ALTER TABLE consents DROP CONSTRAINT consents_channel_check;
ALTER TABLE consents ADD CONSTRAINT consents_channel_check CHECK (channel IN ('email', 'sms', 'push', 'whatsapp', 'in_app', 'ads', 'web'));

-- The company's look, in the Company hub: its logo and colours, which the
-- banner's presets (and anything else on brand) start from. A brand's own
-- look wins over the company's where it is set.
ALTER TABLE workspace_context ADD COLUMN logo_url text NOT NULL DEFAULT '';
ALTER TABLE workspace_context ADD COLUMN colors jsonb NOT NULL DEFAULT '{}';
ALTER TABLE brands ADD COLUMN logo_url text NOT NULL DEFAULT '';
ALTER TABLE brands ADD COLUMN colors jsonb NOT NULL DEFAULT '{}';
