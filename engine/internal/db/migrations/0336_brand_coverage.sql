-- Brand-owned external coverage, commercial records and maintenance work.
-- Money is integer minor units; observations and payments are append-only.

CREATE TABLE coverage_publishers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    domain text NOT NULL CHECK (length(domain) <= 500),
    category text NOT NULL DEFAULT 'publisher' CHECK (length(category) <= 500),
    owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','archived')),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);

CREATE INDEX coverage_publishers_brand_idx ON coverage_publishers (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_agreements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    publisher_id uuid NOT NULL REFERENCES coverage_publishers(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(title) <= 500),
    deal_id uuid REFERENCES deals(id) ON DELETE SET NULL,
    currency text NOT NULL DEFAULT 'USD' CHECK (length(currency) <= 500),
    amount_minor bigint NOT NULL DEFAULT 0 CHECK (amount_minor BETWEEN 0 AND 9000000000000000),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','proposed','active','completed','cancelled')),
    starts_on date,
    ends_on date,
    renewal_on date,
    notice_on date,
    terms text NOT NULL DEFAULT '' CHECK (length(terms) <= 20000),
    owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    approved_version integer,
    approved_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    approved_at timestamptz
);

CREATE INDEX coverage_agreements_brand_idx ON coverage_agreements (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_placements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    publisher_id uuid NOT NULL REFERENCES coverage_publishers(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(title) <= 500),
    url text NOT NULL DEFAULT '' CHECK (length(url) <= 2048),
    inventory text NOT NULL DEFAULT '' CHECK (length(inventory) <= 500),
    target_url text NOT NULL DEFAULT '' CHECK (length(target_url) <= 2048),
    kind text NOT NULL DEFAULT 'blog' CHECK (kind IN ('blog','comparison','news','profile','directory','review','roundup','case_study','partner','integration','resource','interview','podcast','video','community','display_ad','newsletter','sponsorship','custom')),
    arrangement text NOT NULL DEFAULT 'unknown' CHECK (arrangement IN ('earned','paid','exchange','affiliate','unknown')),
    stage text NOT NULL DEFAULT 'identified' CHECK (stage IN ('identified','contacting','discussing','agreed','awaiting_publication','live','declined','cancelled','archived')),
    contact_record_id uuid REFERENCES records(id) ON DELETE SET NULL,
    agreement_id uuid REFERENCES coverage_agreements(id) ON DELETE SET NULL,
    owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    expected_on date,
    published_on date,
    expires_on date,
    expected_link boolean NOT NULL DEFAULT false,
    expected_mention text NOT NULL DEFAULT '' CHECK (length(expected_mention) <= 500),
    sentiment text NOT NULL DEFAULT 'unknown' CHECK (sentiment IN ('positive','neutral','negative','mixed','unknown')),
    tags text NOT NULL DEFAULT '' CHECK (length(tags) <= 500),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000),
    monitor boolean NOT NULL DEFAULT true,
    check_every_hours integer NOT NULL DEFAULT 168 CHECK (check_every_hours BETWEEN 1 AND 8760),
    health text NOT NULL DEFAULT 'unchecked',
    first_verified_at timestamptz,
    last_verified_at timestamptz,
    next_check_at timestamptz NOT NULL DEFAULT now(),
    last_check_at timestamptz,
    failure_count integer NOT NULL DEFAULT 0
);

CREATE INDEX coverage_placements_brand_idx ON coverage_placements (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_deliverables (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    agreement_id uuid NOT NULL REFERENCES coverage_agreements(id) ON DELETE CASCADE,
    placement_id uuid REFERENCES coverage_placements(id) ON DELETE SET NULL,
    title text NOT NULL CHECK (length(title) <= 500),
    quantity numeric NOT NULL DEFAULT 1 CHECK (quantity BETWEEN 0 AND 9000000000000000),
    accepted_quantity numeric NOT NULL DEFAULT 0 CHECK (accepted_quantity BETWEEN 0 AND 9000000000000000),
    unit text NOT NULL DEFAULT 'placement' CHECK (length(unit) <= 500),
    due_on date,
    allocation_minor bigint NOT NULL DEFAULT 0 CHECK (allocation_minor BETWEEN 0 AND 9000000000000000),
    evidence text NOT NULL DEFAULT '' CHECK (length(evidence) <= 20000),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','partial','accepted','cancelled'))
);

CREATE INDEX coverage_deliverables_brand_idx ON coverage_deliverables (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_invoices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    agreement_id uuid NOT NULL REFERENCES coverage_agreements(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(title) <= 500),
    reference text NOT NULL CHECK (length(reference) <= 500),
    kind text NOT NULL DEFAULT 'invoice' CHECK (kind IN ('invoice','credit')),
    currency text NOT NULL DEFAULT 'USD' CHECK (length(currency) <= 500),
    amount_minor bigint NOT NULL DEFAULT 0 CHECK (amount_minor BETWEEN 0 AND 9000000000000000),
    issued_on date,
    due_on date,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','received','disputed','void')),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);

CREATE INDEX coverage_invoices_brand_idx ON coverage_invoices (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_payments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    invoice_id uuid NOT NULL REFERENCES coverage_invoices(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(title) <= 500),
    reference text NOT NULL CHECK (length(reference) <= 500),
    kind text NOT NULL DEFAULT 'payment' CHECK (kind IN ('payment','reversal')),
    currency text NOT NULL DEFAULT 'USD' CHECK (length(currency) <= 500),
    amount_minor bigint NOT NULL DEFAULT 0 CHECK (amount_minor BETWEEN 0 AND 9000000000000000),
    paid_on date NOT NULL,
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);

CREATE INDEX coverage_payments_brand_idx ON coverage_payments (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_rates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    publisher_id uuid NOT NULL REFERENCES coverage_publishers(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(title) <= 500),
    unit text NOT NULL DEFAULT 'placement' CHECK (length(unit) <= 500),
    currency text NOT NULL DEFAULT 'USD' CHECK (length(currency) <= 500),
    amount_minor bigint NOT NULL DEFAULT 0 CHECK (amount_minor BETWEEN 0 AND 9000000000000000),
    minimum_quantity numeric NOT NULL DEFAULT 1 CHECK (minimum_quantity BETWEEN 0 AND 9000000000000000),
    valid_until date,
    status text NOT NULL DEFAULT 'published' CHECK (status IN ('published','negotiated','expired')),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);

CREATE INDEX coverage_rates_brand_idx ON coverage_rates (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_budgets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    title text NOT NULL CHECK (length(title) <= 500),
    currency text NOT NULL DEFAULT 'USD' CHECK (length(currency) <= 500),
    amount_minor bigint NOT NULL DEFAULT 0 CHECK (amount_minor BETWEEN 0 AND 9000000000000000),
    starts_on date NOT NULL,
    ends_on date NOT NULL,
    tags text NOT NULL DEFAULT '' CHECK (length(tags) <= 500),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);

CREATE INDEX coverage_budgets_brand_idx ON coverage_budgets (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    placement_id uuid REFERENCES coverage_placements(id) ON DELETE SET NULL,
    publisher_id uuid REFERENCES coverage_publishers(id) ON DELETE SET NULL,
    title text NOT NULL CHECK (length(title) <= 500),
    reason text NOT NULL DEFAULT 'manual' CHECK (reason IN ('manual','publication_overdue','first_live','page_gone','link_missing','link_changed','outdated_claim','renewal_due','invoice_due','relationship_followup')),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','snoozed','resolved','dismissed')),
    priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('urgent','high','normal','low')),
    owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    due_on date,
    snooze_until date,
    evidence text NOT NULL DEFAULT '' CHECK (length(evidence) <= 20000),
    resolution text NOT NULL DEFAULT '' CHECK (length(resolution) <= 20000)
);

CREATE INDEX coverage_tasks_brand_idx ON coverage_tasks (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_candidates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    url text NOT NULL CHECK (length(url) <= 2048),
    title text NOT NULL CHECK (length(title) <= 500),
    source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','news','backlinks','link_prospects','import')),
    source_id text NOT NULL DEFAULT '' CHECK (length(source_id) <= 500),
    status text NOT NULL DEFAULT 'new' CHECK (status IN ('new','confirmed','dismissed')),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);

CREATE INDEX coverage_candidates_brand_idx ON coverage_candidates (workspace_id, brand_id, created_at DESC, id);

CREATE TABLE coverage_views (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    title text NOT NULL CHECK (length(title) <= 500),
    section text NOT NULL DEFAULT 'placements' CHECK (section IN ('placements','publishers','tasks','agreements','candidates')),
    filters text NOT NULL DEFAULT '{}' CHECK (length(filters) <= 20000),
    visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private','team')),
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX coverage_views_brand_idx ON coverage_views (workspace_id, brand_id, created_at DESC, id);

CREATE UNIQUE INDEX coverage_publishers_domain_idx ON coverage_publishers(workspace_id, brand_id, domain);

CREATE UNIQUE INDEX coverage_placements_url_idx ON coverage_placements(workspace_id, brand_id, url, inventory) WHERE url <> '';

CREATE UNIQUE INDEX coverage_invoices_ref_idx ON coverage_invoices(workspace_id, brand_id, agreement_id, reference);

CREATE UNIQUE INDEX coverage_payments_ref_idx ON coverage_payments(workspace_id, brand_id, invoice_id, reference);

CREATE UNIQUE INDEX coverage_candidates_url_idx ON coverage_candidates(workspace_id, brand_id, url);

CREATE UNIQUE INDEX coverage_tasks_reason_idx ON coverage_tasks(workspace_id, brand_id, placement_id, reason) WHERE reason <> 'manual' AND status IN ('open','in_progress','snoozed');

CREATE INDEX coverage_placements_due_idx ON coverage_placements(next_check_at) WHERE monitor;

CREATE TABLE coverage_observations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    placement_id uuid NOT NULL REFERENCES coverage_placements(id) ON DELETE CASCADE,
    health text NOT NULL,
    source text NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX coverage_observations_placement_idx ON coverage_observations(workspace_id, brand_id, placement_id, created_at DESC);
CREATE TABLE coverage_history (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    resource text NOT NULL,
    resource_id uuid NOT NULL,
    action text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX coverage_history_resource_idx ON coverage_history(workspace_id, brand_id, resource_id, created_at DESC);

ALTER TABLE coverage_placements ADD COLUMN expected_rel text NOT NULL DEFAULT 'any' CHECK (expected_rel IN ('any','follow','nofollow','sponsored','ugc'));

ALTER TABLE coverage_placements ADD COLUMN reader text NOT NULL DEFAULT 'native' CHECK (reader IN ('native','connected'));

ALTER TABLE coverage_placements ADD COLUMN campaign text NOT NULL DEFAULT '' CHECK (length(campaign) <= 500);

ALTER TABLE coverage_placements ADD COLUMN market text NOT NULL DEFAULT '' CHECK (length(market) <= 500);

ALTER TABLE coverage_placements ADD COLUMN language text NOT NULL DEFAULT '' CHECK (length(language) <= 500);

ALTER TABLE coverage_placements ADD COLUMN baseline boolean NOT NULL DEFAULT false;

ALTER TABLE coverage_publishers ADD COLUMN relationship_status text NOT NULL DEFAULT 'new' CHECK (relationship_status IN ('new','introduced','active','dormant','do_not_contact'));

CREATE TABLE coverage_rules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    title text NOT NULL CHECK (length(title) <= 500),
    kind text NOT NULL DEFAULT 'first_live' CHECK (kind IN ('first_live','relationship_followup','page_gone','link_missing','link_changed','publication_overdue','outdated_claim')),
    enabled boolean NOT NULL DEFAULT true,
    owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);
CREATE INDEX coverage_rules_brand_idx ON coverage_rules(workspace_id,brand_id,created_at DESC,id);

CREATE TABLE coverage_finance_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    title text NOT NULL CHECK (length(title) <= 500),
    agreement_id uuid REFERENCES coverage_agreements(id) ON DELETE SET NULL,
    invoice_id uuid REFERENCES coverage_invoices(id) ON DELETE SET NULL,
    publisher_id uuid REFERENCES coverage_publishers(id) ON DELETE SET NULL,
    reason text NOT NULL DEFAULT 'manual' CHECK (reason IN ('manual','renewal_due','notice_due','invoice_due','delivery_overdue')),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','snoozed','resolved','dismissed')),
    priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('normal','urgent','high','low')),
    owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    due_on date,
    snooze_until date,
    evidence text NOT NULL DEFAULT '' CHECK (length(evidence) <= 20000),
    resolution text NOT NULL DEFAULT '' CHECK (length(resolution) <= 20000)
);
CREATE INDEX coverage_finance_tasks_brand_idx ON coverage_finance_tasks(workspace_id,brand_id,created_at DESC,id);

CREATE TABLE coverage_documents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    title text NOT NULL CHECK (length(title) <= 500),
    agreement_id uuid REFERENCES coverage_agreements(id) ON DELETE CASCADE NOT NULL,
    kind text NOT NULL DEFAULT 'contract' CHECK (kind IN ('contract','invoice','media_kit','creative','delivery','other')),
    url text NOT NULL CHECK (length(url) <= 2048),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);
CREATE INDEX coverage_documents_brand_idx ON coverage_documents(workspace_id,brand_id,created_at DESC,id);

CREATE TABLE coverage_delivery_reports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    title text NOT NULL CHECK (length(title) <= 500),
    placement_id uuid REFERENCES coverage_placements(id) ON DELETE CASCADE NOT NULL,
    starts_on date NOT NULL,
    ends_on date NOT NULL,
    source text NOT NULL CHECK (length(source) <= 500),
    evidence text NOT NULL CHECK (length(evidence) <= 20000),
    impressions bigint CHECK (impressions BETWEEN 0 AND 9000000000000000),
    clicks bigint CHECK (clicks BETWEEN 0 AND 9000000000000000),
    sessions bigint CHECK (sessions BETWEEN 0 AND 9000000000000000),
    leads bigint CHECK (leads BETWEEN 0 AND 9000000000000000),
    conversions bigint CHECK (conversions BETWEEN 0 AND 9000000000000000),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);
CREATE INDEX coverage_delivery_reports_brand_idx ON coverage_delivery_reports(workspace_id,brand_id,created_at DESC,id);

CREATE UNIQUE INDEX coverage_rules_kind_idx ON coverage_rules(workspace_id,brand_id,kind);
CREATE UNIQUE INDEX coverage_finance_tasks_evidence_idx ON coverage_finance_tasks(workspace_id,brand_id,reason,evidence) WHERE reason <> 'manual';

CREATE TABLE coverage_report_snapshots (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 title text NOT NULL CHECK(length(title) BETWEEN 1 AND 500),
 data jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX coverage_report_snapshots_brand_idx ON coverage_report_snapshots(workspace_id,brand_id,created_at DESC);

ALTER TABLE coverage_documents ADD COLUMN source_text text NOT NULL DEFAULT '' CHECK(length(source_text)<=20000), ADD COLUMN extraction jsonb, ADD COLUMN extracted_at timestamptz;

-- Process each source record once, including invalid URLs, so bounded batches
-- keep advancing even when many providers describe the same canonical page.
CREATE TABLE coverage_discovery_receipts (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 source text NOT NULL,
 source_id text NOT NULL,
 candidate_id uuid REFERENCES coverage_candidates(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,brand_id,source,source_id)
);

ALTER TABLE coverage_placements ADD COLUMN monitor_accuracy boolean NOT NULL DEFAULT false, ADD COLUMN next_accuracy_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX coverage_placements_accuracy_due_idx ON coverage_placements(next_accuracy_at) WHERE monitor_accuracy AND monitor;
