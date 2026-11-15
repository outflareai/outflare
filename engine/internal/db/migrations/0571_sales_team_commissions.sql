-- The sales team: what each person is asked to bring in a quarter, and
-- what they earn on it. People, their titles and who they report to are
-- the workspace's team directory (staff); these tables hang a brand's
-- numbers on them.

-- A person's quota for a quarter of a brand, in the brand's currency.
CREATE TABLE sales_quotas (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    staff_id     uuid NOT NULL REFERENCES staff(id) ON DELETE CASCADE,
    -- The quarter, such as 2026-Q4.
    period       text NOT NULL CHECK (length(period) BETWEEN 1 AND 20),
    target_minor bigint NOT NULL CHECK (target_minor >= 0),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, staff_id, period)
);
CREATE INDEX sales_quotas_workspace ON sales_quotas (workspace_id);

-- A commission plan: who it covers (job titles, or people by name) and
-- an ordered list of rules. Each rule says what it pays on, when it
-- applies, who gets it and how much; the first rule that applies to a
-- deal or payment pays.
CREATE TABLE commission_plans (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    -- Only an active plan earns anything.
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
    positions    text[] NOT NULL DEFAULT '{}',
    staff_ids    uuid[] NOT NULL DEFAULT '{}',
    -- Days without a payment after which paying again makes someone a
    -- reactivated customer, for rules that tell new from reactivated and
    -- count days since the customer started paying.
    lapse_days   integer NOT NULL DEFAULT 90 CHECK (lapse_days BETWEEN 1 AND 1000),
    rules        jsonb NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX commission_plans_brand ON commission_plans (brand_id, status);
CREATE INDEX commission_plans_workspace ON commission_plans (workspace_id);

-- A person's commission for a month, kept once someone approves it. An
-- open month is worked out when it is read and has no row here; an
-- approved one keeps its lines as they were, whatever changes later.
-- Outflare records that it was paid; it never pays anyone.
CREATE TABLE commission_statements (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    staff_id     uuid NOT NULL REFERENCES staff(id) ON DELETE CASCADE,
    -- The month, such as 2026-11.
    month        text NOT NULL CHECK (length(month) BETWEEN 1 AND 20),
    status       text NOT NULL CHECK (status IN ('approved', 'paid')),
    total_minor  bigint NOT NULL,
    currency     text NOT NULL,
    lines        jsonb NOT NULL DEFAULT '[]',
    approved_by  uuid,
    approved_at  timestamptz NOT NULL DEFAULT now(),
    paid_at      timestamptz,
    UNIQUE (brand_id, staff_id, month)
);
CREATE INDEX commission_statements_workspace ON commission_statements (workspace_id);
