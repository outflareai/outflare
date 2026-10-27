-- Campaign links: one tagging scheme for every link a workspace shares, so
-- visits, sign-ups and revenue can be credited to a channel, source and
-- campaign. A campaign fixes the tags (channel, source, medium and the
-- utm_campaign slug) and has a short code that links carry as ofc, which
-- names the campaign exactly even when someone edits the UTM tags. A link
-- is one destination page in a campaign, with optional content and term.

CREATE TABLE tracking_campaigns (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    -- utm_campaign. Fixed once made, since links in the wild carry it.
    slug         text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,79}$'),
    -- ofc. Random, so it says nothing about the workspace.
    code         text NOT NULL UNIQUE CHECK (code ~ '^[a-z0-9]{10}$'),
    channel      text NOT NULL CHECK (channel IN ('outbound_email', 'seo', 'ai_answers', 'organic_social', 'reddit',
                     'paid_ads', 'affiliates', 'lifecycle_email', 'referral')),
    -- utm_source: the platform, site or partner, such as google or linkedin.
    source       text NOT NULL CHECK (source ~ '^[a-z0-9][a-z0-9._-]{0,59}$'),
    -- utm_medium, from the channel.
    medium       text NOT NULL CHECK (medium ~ '^[a-z0-9][a-z0-9_-]{0,29}$'),
    -- The Outflare object the campaign belongs to, when a module made it:
    -- a sequence, email broadcast, journey, social action, partner program
    -- membership (partner_program), ad campaign or SEO page. '' for campaigns made by hand.
    object_kind  text NOT NULL DEFAULT '' CHECK (object_kind ~ '^[a-z_]{0,40}$'),
    object_id    text NOT NULL DEFAULT '' CHECK (length(object_id) <= 200),
    -- Where links go when no page is given.
    landing_url  text NOT NULL DEFAULT '' CHECK (length(landing_url) <= 2000),
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug)
);

CREATE INDEX tracking_campaigns_ws_idx ON tracking_campaigns (workspace_id, created_at DESC);
CREATE UNIQUE INDEX tracking_campaigns_object_idx ON tracking_campaigns (workspace_id, object_kind, object_id) WHERE object_kind <> '';

CREATE TABLE tracking_links (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    campaign_id  uuid NOT NULL REFERENCES tracking_campaigns (id) ON DELETE CASCADE,
    -- The destination without tags.
    url          text NOT NULL CHECK (length(url) BETWEEN 1 AND 2000),
    -- utm_content and utm_term: which ad, button, post or keyword.
    content      text NOT NULL DEFAULT '' CHECK (content ~ '^[a-z0-9._-]{0,80}$'),
    term         text NOT NULL DEFAULT '' CHECK (term ~ '^[a-z0-9._-]{0,80}$'),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, url, content, term)
);

CREATE INDEX tracking_links_ws_idx ON tracking_links (workspace_id, campaign_id, created_at DESC);
