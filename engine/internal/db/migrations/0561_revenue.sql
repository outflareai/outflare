-- Revenue reporting: where each brand's recurring revenue stands at the
-- end of every day, and how it moved that day (new, expansion,
-- contraction, churned, reactivation), so the Revenue page can show a
-- trend and net revenue retention without re-reading every subscription's
-- history. Money is in the brand's currency, in its minor unit.
CREATE TABLE revenue_days (
    workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id            uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    day                 date NOT NULL,
    currency            text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    -- Where the day ended.
    mrr_minor           bigint NOT NULL DEFAULT 0,
    customers           integer NOT NULL DEFAULT 0,
    -- Recurring revenue of customers whose health score is low.
    at_risk_minor       bigint NOT NULL DEFAULT 0,
    at_risk_customers   integer NOT NULL DEFAULT 0,
    -- How it moved during the day.
    new_minor           bigint NOT NULL DEFAULT 0,
    expansion_minor     bigint NOT NULL DEFAULT 0,
    reactivation_minor  bigint NOT NULL DEFAULT 0,
    contraction_minor   bigint NOT NULL DEFAULT 0,
    churned_minor       bigint NOT NULL DEFAULT 0,
    new_customers       integer NOT NULL DEFAULT 0,
    churned_customers   integer NOT NULL DEFAULT 0,
    -- How many records were in each band of each score at the end of the
    -- day: {"health": {"high": 12, "medium": 30, "low": 4}}.
    bands               jsonb NOT NULL DEFAULT '{}',
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, day)
);
CREATE INDEX revenue_days_workspace ON revenue_days (workspace_id);

-- What each paying person or company brought in a month when revenue was
-- last counted: what the next count compares with to tell new revenue
-- from expansion, and churn from contraction.
CREATE TABLE revenue_customers (
    record_id    uuid PRIMARY KEY REFERENCES records(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    mrr_minor    bigint NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX revenue_customers_brand ON revenue_customers (brand_id);
