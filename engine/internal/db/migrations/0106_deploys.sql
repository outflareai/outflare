-- Deploys: when a website changed. A workspace watches a target on a
-- code or deploy connection (a GitHub repository, a Vercel project),
-- optionally tied to the SEO site it serves, and Outflare records each
-- production deploy it sees. Fix re-checks and experiments start from the
-- deploy instead of guessing when a change went live.
CREATE TABLE deploy_targets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    -- The connector's id for it: owner/name on GitHub, the project id on
    -- Vercel; name is how the provider shows it.
    target          text NOT NULL,
    name            text NOT NULL DEFAULT '',
    url             text NOT NULL DEFAULT '',
    -- The site it serves, so its deploys re-check that site's fixes.
    site_id         uuid REFERENCES seo_sites (id) ON DELETE SET NULL,
    -- The site's brand, kept in step, for brand filters and brand-limited
    -- members. NULL is shared.
    brand_id        uuid REFERENCES brands (id) ON DELETE SET NULL,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    last_checked_at timestamptz,
    last_error      text,
    -- The newest deploy seen, so the next check reads only newer ones.
    last_deploy_at  timestamptz,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (integration_id, target)
);
CREATE INDEX deploy_targets_ws_idx ON deploy_targets (workspace_id);
CREATE INDEX deploy_targets_site_idx ON deploy_targets (site_id) WHERE site_id IS NOT NULL;

CREATE TABLE deploys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    target_id     uuid NOT NULL REFERENCES deploy_targets (id) ON DELETE CASCADE,
    -- Copied from the target when seen, so history keeps its site.
    site_id       uuid REFERENCES seo_sites (id) ON DELETE SET NULL,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    external_id   text NOT NULL,
    -- deployment (the provider's record) or push (a commit to the
    -- production branch of a repository that records no deploys).
    kind          text NOT NULL,
    state         text NOT NULL CHECK (state IN ('ready', 'failed', 'building')),
    commit_sha    text NOT NULL DEFAULT '',
    ref           text NOT NULL DEFAULT '',
    message       text NOT NULL DEFAULT '',
    url           text NOT NULL DEFAULT '',
    -- When it went live (ready) or was created.
    deployed_at   timestamptz NOT NULL,
    seen_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (target_id, external_id)
);
CREATE INDEX deploys_ws_idx ON deploys (workspace_id, deployed_at DESC);
CREATE INDEX deploys_site_idx ON deploys (site_id, deployed_at DESC) WHERE site_id IS NOT NULL;
