-- Payments: who pays the workspace, how much and for what, read from its
-- own billing accounts (Stripe first). Customers are person records (a
-- stripe.customer identity), payments and refunds are events on their
-- timeline, and their plan, monthly and lifetime revenue and billing status
-- are computed fields. These tables hold only what those need: where each
-- connection's sync got to, and each subscription's current state, which
-- monthly revenue is worked out from and which partner commissions will
-- read.

-- One row per payment connection.
CREATE TABLE payment_syncs (
    integration_id  uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- backfill: listing everything once; changes: reading what changed.
    phase           text NOT NULL DEFAULT 'backfill' CHECK (phase IN ('backfill', 'changes')),
    -- Backfill: the kind being listed and the page cursor within it.
    backfill_kind   text,
    backfill_cursor text,
    -- The provider's change cursor, taken before the backfill starts so
    -- nothing that changes during it is missed.
    changes_cursor  text,
    -- Totals since the connection was made: customers, subscriptions,
    -- payments, refunds, records created.
    totals          jsonb NOT NULL DEFAULT '{}',
    last_run_at     timestamptz,
    last_success_at timestamptz,
    last_error      text,
    backfilled_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX payment_syncs_workspace_idx ON payment_syncs (workspace_id);

-- A subscription's current state. History is the subscription.* events.
CREATE TABLE payment_subscriptions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    -- stripe, and later sheepy, paddle.
    provider        text NOT NULL,
    provider_id     text NOT NULL,
    record_id       uuid REFERENCES records (id) ON DELETE SET NULL,
    customer_id     text NOT NULL,
    status          text NOT NULL CHECK (status IN ('trialing', 'active', 'past_due', 'canceled', 'other')),
    plan            text NOT NULL DEFAULT '',
    product_id      text NOT NULL DEFAULT '',
    price_id        text NOT NULL DEFAULT '',
    billing_interval text NOT NULL DEFAULT '',
    -- What it brings in a month, before discounts and tax.
    monthly_minor   bigint NOT NULL DEFAULT 0,
    currency        text NOT NULL DEFAULT '',
    started_at      timestamptz NOT NULL,
    canceled_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (integration_id, provider_id)
);

CREATE INDEX payment_subscriptions_record_idx ON payment_subscriptions (record_id) WHERE record_id IS NOT NULL;
CREATE INDEX payment_subscriptions_customer_idx ON payment_subscriptions (integration_id, customer_id);
