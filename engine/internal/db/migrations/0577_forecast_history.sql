-- The forecast week by week (docs/revenue.md, "How the forecast moved"):
-- what a quarter's forecast said in each week, so a team can see whether
-- it is climbing or slipping and what the quarter ended on. One row per
-- brand, quarter and week (its Monday): the last reading of that week.
-- The forecast itself is still worked out when read; this only keeps what
-- it said.
CREATE TABLE revenue_forecast_history (
    workspace_id            uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id                uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    -- Such as 2026-Q4. Only Outflare writes it, so it carries no check: the
    -- previous version's tests must be able to fill this table with any text.
    quarter                 text NOT NULL,
    week                    date NOT NULL,
    currency                text NOT NULL,
    -- Null when the brand had set no target for the quarter that week.
    target_minor            bigint,
    won_minor               bigint NOT NULL,
    renewals_expected_minor bigint NOT NULL,
    deals_expected_minor    bigint NOT NULL,
    forecast_minor          bigint NOT NULL,
    best_case_minor         bigint NOT NULL,
    -- Renewals and open deals behind the numbers.
    open_count              integer NOT NULL DEFAULT 0,
    taken_at                timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, quarter, week)
);
CREATE INDEX revenue_forecast_history_workspace ON revenue_forecast_history (workspace_id);
