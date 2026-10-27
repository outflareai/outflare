-- How far agents may go on their own with the fixes list. An agent takes a
-- fix (POST .../fixes/{fix}/start) before it changes the site; in a
-- mission, this setting decides whether that runs or waits for a person:
-- copilot (the default) holds every fix, rules lets the listed rules and
-- categories through, autonomous lets every fix through. Fixes that touch
-- noindex, canonicals, redirects or robots.txt always wait for a person.
-- A site's row overrides the workspace's (site_id null).
--
-- verify_on_deploy: a production deploy of the site starts a crawl that
-- confirms or reopens its fixes.

CREATE TABLE fix_autonomy (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id          uuid REFERENCES seo_sites (id) ON DELETE CASCADE,
    mode             text NOT NULL DEFAULT 'copilot' CHECK (mode IN ('copilot', 'rules', 'autonomous')),
    -- Crawl rule ids (title_too_long) or categories (metadata) that run in
    -- rules mode.
    allowed          text[] NOT NULL DEFAULT '{}',
    verify_on_deploy boolean NOT NULL DEFAULT true,
    updated_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX fix_autonomy_workspace_idx ON fix_autonomy (workspace_id) WHERE site_id IS NULL;
CREATE UNIQUE INDEX fix_autonomy_site_idx ON fix_autonomy (site_id) WHERE site_id IS NOT NULL;
