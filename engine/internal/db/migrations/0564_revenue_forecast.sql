-- The forecast's settings, one row per brand: the target for each
-- quarter and how much weight a renewal or an open deal gets. Empty
-- weights mean the defaults (docs/revenue.md, "Forecast"). The forecast
-- itself is worked out when read and never stored.
CREATE TABLE revenue_forecast_settings (
    brand_id        uuid PRIMARY KEY REFERENCES brands(id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- Quarter to target in the brand's minor unit: {"2026-Q4": 12000000}.
    targets         jsonb NOT NULL DEFAULT '{}',
    -- Percent of a renewal's value counted, by the company's health band:
    -- {"high": 95, "medium": 80, "low": 50, "none": 85}.
    renewal_weights jsonb NOT NULL DEFAULT '{}',
    -- Percent of an open deal's value counted, by its stage's key.
    stage_weights   jsonb NOT NULL DEFAULT '{}',
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX revenue_forecast_settings_workspace ON revenue_forecast_settings (workspace_id);
