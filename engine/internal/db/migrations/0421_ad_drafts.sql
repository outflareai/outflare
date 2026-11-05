-- New ads (step 6 of the paid ads plan): ads written by the brand agent or
-- a person, kept as drafts until a person approves them. Approving makes
-- them on the platform paused (a job, ads.create); a person then turns
-- them on (ads.go_live). A draft is a new ad in an ad group, a new ad
-- group with keywords and an ad, or a new search campaign.
--
-- Brand data from the start: every draft belongs to one brand.

CREATE TABLE ad_drafts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    ad_account_id uuid NOT NULL REFERENCES ad_accounts(id) ON DELETE CASCADE,
    -- The campaign the new ad or ad group goes in (null for a new
    -- campaign), and the ad group a new ad goes in.
    campaign_entity_id uuid REFERENCES ad_entities(id) ON DELETE CASCADE,
    ad_group_entity_id uuid REFERENCES ad_entities(id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('ad', 'ad_group', 'campaign')),
    -- What to make: names, the ad's link and text, keywords, a new
    -- campaign's budget, bidding and countries (connectors.AdDraft).
    content       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(content) = 'object'),
    -- Why the writer thinks it will do better, in a sentence or two.
    reason        text NOT NULL DEFAULT '',
    -- draft (waits for a person), creating (approved; the job is making
    -- it), paused (made on the platform, paused), going_live (the job is
    -- turning it on), live, failed (the platform refused it), dismissed.
    status        text NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'creating', 'paused', 'going_live', 'live', 'failed', 'dismissed')),
    -- The platform's ids of what was made, and which levels the draft made.
    made          jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(made) = 'object'),
    error         text NOT NULL DEFAULT '',
    -- Who wrote it (and the agent's action), who approved and who turned
    -- it on.
    created_by    uuid REFERENCES actors(id) ON DELETE SET NULL,
    action_id     uuid REFERENCES actions(id) ON DELETE SET NULL,
    approved_by   uuid REFERENCES actors(id) ON DELETE SET NULL,
    approved_at   timestamptz,
    made_at       timestamptz,
    live_by       uuid REFERENCES actors(id) ON DELETE SET NULL,
    live_at       timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ad_drafts_brand_idx ON ad_drafts (workspace_id, brand_id, created_at DESC);
CREATE INDEX ad_drafts_campaign_idx ON ad_drafts (campaign_entity_id) WHERE campaign_entity_id IS NOT NULL;
