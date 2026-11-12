-- Changes to a Google Ads account itself rather than to one campaign
-- (docs/paid-media.md, Required Minimum Functionality): a website
-- conversion action created, an account-level callout added or removed,
-- a portfolio bidding strategy created or its target moved. Like
-- ad_changes, each is a row the API makes in the same transaction as its
-- audit event, carried out by the ad sync's change job (the platform call
-- is the side effect) and read back afterwards. result is what the
-- platform made: the new conversion action's, callout's or strategy's id.
--
-- Expand only (docs/migrations.md): one new table. The previous version
-- ignores it.
CREATE TABLE ad_account_changes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The account's brand; null when the account is shared by every brand.
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    ad_account_id uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('conversion_action_create', 'callout_add', 'callout_remove',
        'bidding_strategy_create', 'bidding_strategy_update')),
    request       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(request) = 'object'),
    result        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(result) = 'object'),
    status        text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'applied', 'failed')),
    error         text NOT NULL DEFAULT '',
    verified_at   timestamptz,
    verify_note   text NOT NULL DEFAULT '',
    actor_id      uuid REFERENCES actors (id) ON DELETE SET NULL,
    action_id     uuid REFERENCES actions (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    applied_at    timestamptz
);
CREATE INDEX ad_account_changes_account_idx ON ad_account_changes (ad_account_id, created_at DESC);
CREATE INDEX ad_account_changes_workspace_idx ON ad_account_changes (workspace_id, created_at DESC);
