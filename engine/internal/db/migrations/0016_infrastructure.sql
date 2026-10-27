-- Infrastructure lifecycle: "10 domains and 30 mailboxes for this project
-- by the 14th" is one plan that a person, or an agent with the approve
-- scope, approves. The engine then buys the domains and mailboxes through
-- a supplier (or waits for the person to buy them), checks DNS, connects
-- the mailboxes, warms them up, and moves them through their working life:
-- ready to send, resting, retired.

CREATE TABLE infra_plans (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id            uuid REFERENCES projects (id) ON DELETE SET NULL,
    name                  text NOT NULL,
    -- draft: editable, nothing bought. building: approved; buying, DNS and
    -- connecting. warming: every mailbox is connected and warming up.
    -- ready: every mailbox can send. cancelled: stopped by a person.
    status                text NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('draft', 'building', 'warming', 'ready', 'cancelled')),
    -- Who buys: 'manual' (the person buys and the plan waits for them) or
    -- a supplier connector such as 'cheapinboxes'. Customers pay suppliers
    -- directly, on their own accounts.
    supplier              text NOT NULL DEFAULT 'manual',
    -- The workspace's connected supplier account (a connectors integration).
    -- No foreign key yet: the connectors table lands on another branch.
    supplier_integration_id uuid,
    provider              text NOT NULL DEFAULT 'google' CHECK (provider IN ('google', 'microsoft')),
    domain_count          integer NOT NULL CHECK (domain_count BETWEEN 1 AND 200),
    mailboxes_per_domain  integer NOT NULL CHECK (mailboxes_per_domain BETWEEN 1 AND 10),
    -- The words domain names are made from, e.g. the company name.
    brand                 text NOT NULL DEFAULT '',
    -- The people the mailboxes send as: [{first_name, last_name}].
    personas              jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(personas) = 'array'),
    ready_by              date,
    -- Days of warm-up before a mailbox may send cold email.
    warmup_days           integer NOT NULL DEFAULT 14 CHECK (warmup_days BETWEEN 7 AND 60),
    -- Cold emails a day per mailbox once it is ready.
    mailbox_daily_limit   integer NOT NULL DEFAULT 30 CHECK (mailbox_daily_limit BETWEEN 1 AND 500),
    -- A ready mailbox whose warm-up inbox placement falls below this
    -- percent rests until it recovers.
    rest_below            integer NOT NULL DEFAULT 75 CHECK (rest_below BETWEEN 0 AND 100),
    -- Retire a mailbox that has rested this many days without recovering.
    -- Null never retires on its own. Retiring never cancels at the supplier.
    retire_after_days     integer CHECK (retire_after_days BETWEEN 3 AND 120),
    notes                 text NOT NULL DEFAULT '',
    -- The supplier's price for the plan: {status: pending|ready|failed|none,
    -- currency, total_cents, monthly_cents, once_cents, domains: [...], error}.
    quote                 jsonb NOT NULL DEFAULT '{"status": "none"}',
    -- The supplier's order: when the engine started placing it, and the
    -- supplier's reference once placed. Started without a reference means
    -- the order may or may not have gone through, and is never retried.
    order_started_at      timestamptz,
    order_ref             text,
    created_by            uuid REFERENCES actors (id),
    approved_by           uuid REFERENCES actors (id),
    approved_at           timestamptz,
    cancelled_at          timestamptz,
    completed_at          timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX infra_plans_ws_idx ON infra_plans (workspace_id, created_at DESC);
CREATE INDEX infra_plans_active_idx ON infra_plans (status) WHERE status IN ('building', 'warming');

-- One row for each domain and each mailbox a plan builds. Its stage is the
-- build: once a mailbox is connected, the mailbox row's lifecycle_stage
-- takes over.
CREATE TABLE infra_plan_items (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    plan_id          uuid NOT NULL REFERENCES infra_plans (id) ON DELETE CASCADE,
    kind             text NOT NULL CHECK (kind IN ('domain', 'mailbox')),
    -- The domain, or the mailbox's address.
    name             text NOT NULL,
    domain           text NOT NULL,
    -- A domain the workspace already had: nothing to buy, only DNS to check
    -- and mailboxes to add.
    owned            boolean NOT NULL DEFAULT false,
    first_name       text NOT NULL DEFAULT '',
    last_name        text NOT NULL DEFAULT '',
    -- planned: in the plan, nothing done. buying: ordered from the supplier,
    -- or waiting for the person to buy it. dns: bought; waiting for SPF, DKIM
    -- and DMARC to pass. connecting: waiting for the mailbox to be connected.
    -- done: built. failed: the supplier could not build it. cancelled.
    stage            text NOT NULL DEFAULT 'planned'
                     CHECK (stage IN ('planned', 'buying', 'dns', 'connecting', 'done', 'failed', 'cancelled')),
    -- Why it is in its stage, or what is left to do, in plain words.
    detail           text NOT NULL DEFAULT '',
    -- The supplier's answer to the quote: available for purchase or not.
    available        boolean,
    price_cents      bigint,
    mailbox_id       uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    stage_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX infra_plan_items_name_idx ON infra_plan_items (plan_id, lower(name));
CREATE INDEX infra_plan_items_ws_name_idx ON infra_plan_items (workspace_id, lower(name));

-- Every mailbox's place in its working life. warming: warming up, no cold
-- email yet. ready: may send cold email. resting: no cold email for a while,
-- warm-up continues. retired: out of use for good (paused, warm-up off).
-- Mailboxes that existed before plans are ready.
ALTER TABLE mailboxes
    ADD COLUMN lifecycle_stage  text NOT NULL DEFAULT 'ready'
                                CHECK (lifecycle_stage IN ('warming', 'ready', 'resting', 'retired')),
    ADD COLUMN stage_reason     text NOT NULL DEFAULT '',
    ADD COLUMN stage_changed_at timestamptz NOT NULL DEFAULT now(),
    -- warming: the earliest it can be ready. resting: when the rest ends.
    ADD COLUMN stage_until      timestamptz,
    -- Rest on its own when warm-up placement drops, and come back when it
    -- recovers. On for mailboxes a plan built.
    ADD COLUMN auto_rest        boolean NOT NULL DEFAULT false,
    ADD COLUMN infra_plan_id    uuid REFERENCES infra_plans (id) ON DELETE SET NULL;

CREATE INDEX mailboxes_lifecycle_idx ON mailboxes (lifecycle_stage) WHERE lifecycle_stage <> 'ready';
CREATE INDEX mailboxes_infra_plan_idx ON mailboxes (infra_plan_id) WHERE infra_plan_id IS NOT NULL;

-- plan: added by an infrastructure plan.
ALTER TABLE sending_domains DROP CONSTRAINT sending_domains_source_check,
    ADD CONSTRAINT sending_domains_source_check CHECK (source IN ('mailbox', 'manual', 'plan'));
