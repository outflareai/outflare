-- Paid link orders, built on Coverage (donor catalog, settings, orders).
-- New tables only: Presence publishers and rates keep their public schema,
-- so existing Coverage forms and clients stay as they are. The PBN flag is
-- a manual column; nothing here detects or hides a PBN automatically.
-- Only adds, so the version before still works.

-- A catalog profile beside a Coverage publisher, created when the domain
-- is used as a paid-link donor. One profile per publisher.
CREATE TABLE coverage_donor_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    publisher_id uuid NOT NULL REFERENCES coverage_publishers(id) ON DELETE CASCADE,
    pbn boolean NOT NULL DEFAULT false,
    country text NOT NULL DEFAULT '' CHECK (length(country) <= 100),
    languages text[] NOT NULL DEFAULT '{}',
    niches text[] NOT NULL DEFAULT '{}',
    casino_allowed boolean NOT NULL DEFAULT false,
    rel_offered text[] NOT NULL DEFAULT '{}',
    slot_types text[] NOT NULL DEFAULT '{}',
    duration text NOT NULL DEFAULT '' CHECK (length(duration) <= 100),
    duration_days integer CHECK (duration_days IS NULL OR duration_days >= 0),
    tat_days integer CHECK (tat_days IS NULL OR tat_days >= 0),
    guarantee_days integer CHECK (guarantee_days IS NULL OR guarantee_days >= 0),
    vendor_id text NOT NULL DEFAULT '' CHECK (length(vendor_id) <= 200),
    source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'csv', 'competitor_gap')),
    domain_rating integer CHECK (domain_rating IS NULL OR domain_rating BETWEEN 0 AND 100),
    traffic bigint CHECK (traffic IS NULL OR traffic >= 0),
    spam_score numeric,
    metrics_at timestamptz,
    metrics_source text NOT NULL DEFAULT '' CHECK (length(metrics_source) <= 100),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000),
    archived_at timestamptz,
    created_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    updated_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX coverage_donor_profiles_publisher_idx
    ON coverage_donor_profiles (workspace_id, brand_id, publisher_id);
CREATE INDEX coverage_donor_profiles_brand_idx
    ON coverage_donor_profiles (workspace_id, brand_id, archived_at);
CREATE INDEX coverage_donor_profiles_pbn_idx
    ON coverage_donor_profiles (workspace_id, brand_id, pbn);
CREATE INDEX coverage_donor_profiles_niches_idx
    ON coverage_donor_profiles USING GIN (niches);

-- Who may approve a paid link order and mark it paid, and the domain-rating
-- band used to find an equivalent donor when a link is lost. An empty
-- approver leaves Approve and Paid blocked until someone with
-- coverage.approve sets one. The person is a workspace member, not a
-- hard-coded id.
CREATE TABLE coverage_link_order_settings (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    approver_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    equivalent_dr_band integer NOT NULL DEFAULT 10 CHECK (equivalent_dr_band BETWEEN 1 AND 50),
    updated_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, brand_id)
);

-- The order's own status machine (not coverage_placements.stage).
-- Amounts are minor units; USD and EUR are never summed. Paid is gated in
-- the application: after publication and a passing live-link check, by the
-- brand's approver. Later pull requests wire the API and jobs.
CREATE TABLE coverage_link_orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    publisher_id uuid NOT NULL REFERENCES coverage_publishers(id) ON DELETE RESTRICT,
    donor_profile_id uuid NOT NULL REFERENCES coverage_donor_profiles(id) ON DELETE RESTRICT,
    target_url text NOT NULL CHECK (length(target_url) BETWEEN 1 AND 2048),
    slot_type text NOT NULL DEFAULT '' CHECK (length(slot_type) <= 100),
    rel_requested text NOT NULL DEFAULT '' CHECK (length(rel_requested) <= 50),
    status text NOT NULL DEFAULT 'requested' CHECK (status IN (
        'requested', 'quoted', 'negotiating', 'approved', 'published', 'paid', 'verified', 'lost'
    )),
    quoted_amount_minor bigint CHECK (quoted_amount_minor IS NULL OR quoted_amount_minor >= 0),
    quoted_currency text CHECK (quoted_currency IS NULL OR quoted_currency IN ('USD', 'EUR')),
    donor_amount_minor bigint CHECK (donor_amount_minor IS NULL OR donor_amount_minor >= 0),
    currency text CHECK (currency IS NULL OR currency IN ('USD', 'EUR')),
    guarantee_days integer CHECK (guarantee_days IS NULL OR guarantee_days >= 0),
    guarantee_until date,
    published_url text NOT NULL DEFAULT '' CHECK (length(published_url) <= 2048),
    proof_url text NOT NULL DEFAULT '' CHECK (length(proof_url) <= 2048),
    published_at timestamptz,
    placement_id uuid REFERENCES coverage_placements(id) ON DELETE SET NULL,
    agreement_id uuid REFERENCES coverage_agreements(id) ON DELETE SET NULL,
    invoice_id uuid REFERENCES coverage_invoices(id) ON DELETE SET NULL,
    payment_id uuid REFERENCES coverage_payments(id) ON DELETE SET NULL,
    credit_payment_id uuid REFERENCES coverage_payments(id) ON DELETE SET NULL,
    replaces_order_id uuid REFERENCES coverage_link_orders(id) ON DELETE SET NULL,
    owner_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    last_check_at timestamptz,
    last_check_live boolean,
    last_check_target_match boolean,
    last_check_rel text NOT NULL DEFAULT '' CHECK (length(last_check_rel) <= 50),
    last_check_anchor text NOT NULL DEFAULT '' CHECK (length(last_check_anchor) <= 500),
    approved_at timestamptz,
    approved_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    paid_at timestamptz,
    paid_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    lost_at timestamptz,
    lost_reason text NOT NULL DEFAULT '' CHECK (length(lost_reason) <= 2000),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000),
    created_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    updated_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX coverage_link_orders_brand_status_idx
    ON coverage_link_orders (workspace_id, brand_id, status, created_at DESC);
CREATE INDEX coverage_link_orders_publisher_idx
    ON coverage_link_orders (workspace_id, brand_id, publisher_id);
CREATE INDEX coverage_link_orders_replaces_idx
    ON coverage_link_orders (replaces_order_id) WHERE replaces_order_id IS NOT NULL;
CREATE INDEX coverage_link_orders_guarantee_idx
    ON coverage_link_orders (workspace_id, brand_id, guarantee_until)
    WHERE status IN ('published', 'paid', 'verified') AND guarantee_until IS NOT NULL;

-- Status and field history for an order, so later work does not need a
-- second migration if generic coverage_history is not a fit.
CREATE TABLE coverage_link_order_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    order_id uuid NOT NULL REFERENCES coverage_link_orders(id) ON DELETE CASCADE,
    actor_id uuid REFERENCES actors(id) ON DELETE SET NULL,
    from_status text,
    to_status text,
    payload jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX coverage_link_order_events_order_idx
    ON coverage_link_order_events (workspace_id, brand_id, order_id, created_at DESC);
