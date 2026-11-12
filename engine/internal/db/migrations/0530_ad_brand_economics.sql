-- Paid ads intelligence layer (plans/paid-ads/plan.md): the brand's own
-- economics, which every finding, verdict and report is scored in. The
-- owner gives two numbers, what a new paying customer may cost and what
-- a sign-up may cost, plus the window after an ad click in which a
-- sign-up or first payment still counts for it, and how long a new
-- customer's revenue is expected to take to pay back its cost. Every
-- automatic rule starts in shadow mode, recording what it would have
-- done until the owner turns it live. Nothing here lets a change apply
-- without a person: only an extension's hook can (Host.SetAdAutoApprove).
--
-- Expand only (docs/migrations.md): one new table. The previous version
-- ignores it.

CREATE TABLE ad_brand_economics (
    brand_id                         uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id                     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    currency                         text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    -- The owner's two numbers. Micros of currency. Null: not set yet.
    target_cost_per_payer_micros     bigint CHECK (target_cost_per_payer_micros > 0),
    target_cost_per_signup_micros    bigint CHECK (target_cost_per_signup_micros > 0),
    -- Days after an ad click a sign-up or first payment still counts for it.
    acquisition_window_days          integer NOT NULL DEFAULT 30 CHECK (acquisition_window_days BETWEEN 1 AND 365),
    -- Months the brand expects a new customer's revenue to pay back its cost.
    payback_months                   integer NOT NULL DEFAULT 12 CHECK (payback_months BETWEEN 1 AND 60),
    gross_margin_percent             integer CHECK (gross_margin_percent BETWEEN 1 AND 100),
    -- Rules only record what they would do until this is off.
    shadow_mode                      boolean NOT NULL DEFAULT true,
    updated_by                       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at                       timestamptz NOT NULL DEFAULT now(),
    updated_at                       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ad_brand_economics_ws_idx ON ad_brand_economics (workspace_id);
