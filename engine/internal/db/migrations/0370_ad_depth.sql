-- More of what Meta and Google report. Delivery numbers beside spend
-- (null where the platform didn't say), conversions by the platform's own
-- action, and Google's search keywords and the searches they matched.
-- Like every ad number, these are the platform's claims. Each row carries
-- its campaign's brand (or its account's), set by the sync
-- (plans/brands/README.md).

-- Reach is per day: someone reached on two days counts on both.
ALTER TABLE ad_metrics_daily
    ADD COLUMN reach              bigint CHECK (reach >= 0),
    ADD COLUMN link_clicks        bigint CHECK (link_clicks >= 0),
    ADD COLUMN landing_page_views bigint CHECK (landing_page_views >= 0),
    ADD COLUMN video_views        bigint CHECK (video_views >= 0);

-- One row per campaign, ad group or ad, day and action. Meta's kinds are
-- purchase, lead and so on, each counted once, and custom.<id> for a
-- custom conversion; Google's are its conversion actions' names.
CREATE TABLE ad_conversion_actions_daily (
    ad_entity_id uuid NOT NULL REFERENCES ad_entities (id) ON DELETE CASCADE,
    day          date NOT NULL,
    action       text NOT NULL CHECK (length(action) BETWEEN 1 AND 300),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    conversions  numeric(18, 4) NOT NULL DEFAULT 0,
    value_micros bigint,
    PRIMARY KEY (ad_entity_id, day, action)
);
CREATE INDEX ad_conversion_actions_daily_ws_idx ON ad_conversion_actions_daily (workspace_id, day);

-- A Google search keyword in an ad group, and its numbers per day. Money
-- is in micros of the account's currency, which the sync copies from the
-- account (already checked there).
CREATE TABLE ad_keywords (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    ad_account_id uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    ad_group_id   uuid NOT NULL REFERENCES ad_entities (id) ON DELETE CASCADE,
    external_id   text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 100),
    text          text NOT NULL DEFAULT '',
    match_type    text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'other' CHECK (status IN ('active', 'paused', 'removed', 'other')),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (ad_group_id, external_id)
);
CREATE INDEX ad_keywords_ws_idx ON ad_keywords (workspace_id);

CREATE TABLE ad_keyword_metrics_daily (
    keyword_id              uuid NOT NULL REFERENCES ad_keywords (id) ON DELETE CASCADE,
    day                     date NOT NULL,
    workspace_id            uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id                uuid REFERENCES brands (id) ON DELETE SET NULL,
    currency                text NOT NULL,
    spend_micros            bigint NOT NULL DEFAULT 0 CHECK (spend_micros >= 0),
    impressions             bigint NOT NULL DEFAULT 0 CHECK (impressions >= 0),
    clicks                  bigint NOT NULL DEFAULT 0 CHECK (clicks >= 0),
    conversions             numeric(18, 4),
    conversion_value_micros bigint,
    PRIMARY KEY (keyword_id, day)
);
CREATE INDEX ad_keyword_metrics_daily_ws_idx ON ad_keyword_metrics_daily (workspace_id, day);

-- What people searched that showed an ad, per ad group and day, with the
-- keyword it matched. Google leaves out searches too few people made.
CREATE TABLE ad_search_terms_daily (
    workspace_id            uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id                uuid REFERENCES brands (id) ON DELETE SET NULL,
    ad_account_id           uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    ad_group_id             uuid NOT NULL REFERENCES ad_entities (id) ON DELETE CASCADE,
    day                     date NOT NULL,
    term                    text NOT NULL CHECK (length(term) BETWEEN 1 AND 300),
    keyword_text            text NOT NULL DEFAULT '',
    keyword_match_type      text NOT NULL DEFAULT '',
    currency                text NOT NULL,
    spend_micros            bigint NOT NULL DEFAULT 0 CHECK (spend_micros >= 0),
    impressions             bigint NOT NULL DEFAULT 0 CHECK (impressions >= 0),
    clicks                  bigint NOT NULL DEFAULT 0 CHECK (clicks >= 0),
    conversions             numeric(18, 4),
    conversion_value_micros bigint,
    PRIMARY KEY (ad_group_id, day, term, keyword_text, keyword_match_type)
);
CREATE INDEX ad_search_terms_daily_ws_idx ON ad_search_terms_daily (workspace_id, day);
