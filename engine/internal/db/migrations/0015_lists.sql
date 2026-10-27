-- Building lists from lead data providers. A list can be built from a
-- search at a connected provider (Apollo, People Data Labs, ...), with
-- missing emails found by a waterfall of email finders, and refreshed on a
-- schedule. Outflare keeps no contact database of its own: people enter the
-- workspace only when they become leads in one of its lists.

ALTER TABLE lead_lists
    ADD COLUMN description text NOT NULL DEFAULT '',
    -- manual: leads are added by import, API or by hand. search: the list
    -- is built from a provider search and can be refreshed.
    ADD COLUMN kind text NOT NULL DEFAULT 'manual' CHECK (kind IN ('manual', 'search')),
    -- The integration that runs the search, and the search in shared
    -- filter terms (titles, seniorities, industries, employee_ranges,
    -- locations, company_locations, company_domains, keywords).
    ADD COLUMN search_integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    ADD COLUMN filters jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(filters) = 'object'),
    -- Integrations that find missing emails, tried in order until one
    -- answers (the waterfall). Empty: the search provider, if it can.
    ADD COLUMN enrich_integration_ids uuid[] NOT NULL DEFAULT '{}',
    -- Most new leads one build or refresh adds.
    ADD COLUMN max_leads integer NOT NULL DEFAULT 500 CHECK (max_leads BETWEEN 1 AND 50000),
    -- Most provider credits one build or refresh may spend. Null: no cap
    -- beyond max_leads.
    ADD COLUMN credit_cap numeric CHECK (credit_cap >= 0),
    -- Check new addresses with the workspace's email verification before
    -- adding them, and leave out the ones its settings block.
    ADD COLUMN verify boolean NOT NULL DEFAULT true,
    -- Only add people whose address the provider itself verified.
    ADD COLUMN verified_emails_only boolean NOT NULL DEFAULT false,
    -- Leave out people any sequence emailed in this many days (0: off),
    -- and people active in another sequence, so campaigns and clients do
    -- not double up.
    ADD COLUMN skip_contacted_days integer NOT NULL DEFAULT 90 CHECK (skip_contacted_days BETWEEN 0 AND 3650),
    ADD COLUMN skip_in_sequences boolean NOT NULL DEFAULT true,
    -- Leave out people whose ICP fit score is below this, before spending
    -- credits on them. 0: keep everyone.
    ADD COLUMN min_score smallint NOT NULL DEFAULT 0 CHECK (min_score BETWEEN 0 AND 100),
    -- Refresh: run the search again every N days and add people who are
    -- new. Null: only when asked.
    ADD COLUMN refresh_every_days integer CHECK (refresh_every_days BETWEEN 1 AND 365),
    ADD COLUMN next_refresh_at timestamptz,
    ADD COLUMN created_by uuid REFERENCES actors (id) ON DELETE SET NULL,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX lead_lists_refresh_idx ON lead_lists (next_refresh_at) WHERE next_refresh_at IS NOT NULL;

-- How well each member fits the list's ideal customer profile, 0 to 100,
-- with the reasons, and the run that added it.
ALTER TABLE lead_list_members
    ADD COLUMN score smallint CHECK (score BETWEEN 0 AND 100),
    ADD COLUMN score_reasons jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN run_id uuid;

-- One build, refresh, enrichment or preview of a list. Runs call providers,
-- so they happen in jobs; the row is the live progress and the record.
CREATE TABLE list_runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Null for a preview of a search that is not a list yet.
    list_id         uuid REFERENCES lead_lists (id) ON DELETE CASCADE,
    project_id      uuid REFERENCES projects (id) ON DELETE SET NULL,
    -- preview: one page of results, nothing saved and no credits spent on
    -- emails. build and refresh: search, find emails, add new leads.
    -- enrich: fill in missing fields on the list's leads.
    kind            text NOT NULL CHECK (kind IN ('preview', 'build', 'refresh', 'enrich')),
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    -- What the run was asked to do: search integration, filters, waterfall
    -- and limits, copied so the record stays true if the list changes.
    params          jsonb NOT NULL DEFAULT '{}',
    found           integer NOT NULL DEFAULT 0, -- people the search returned
    total_matches   integer,                    -- matches the provider reported
    added           integer NOT NULL DEFAULT 0, -- new members of the list
    enriched        integer NOT NULL DEFAULT 0, -- people a provider filled in
    -- Why people were left out: {no_email, suppressed, contacted,
    -- in_sequence, undeliverable, unverified, low_score, already_in_list}.
    skipped         jsonb NOT NULL DEFAULT '{}',
    credits         numeric NOT NULL DEFAULT 0,
    -- Credits by integration id, for the spend ledger.
    credits_by      jsonb NOT NULL DEFAULT '{}',
    -- Preview results.
    sample          jsonb NOT NULL DEFAULT '[]',
    -- Why it stopped early, in words a person can act on.
    stop_reason     text,
    error           text,
    requested_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    finished_at     timestamptz
);

CREATE INDEX list_runs_list_idx ON list_runs (list_id, created_at DESC);
CREATE INDEX list_runs_ws_idx ON list_runs (workspace_id, created_at DESC);
-- One build or refresh at a time per list.
CREATE UNIQUE INDEX list_runs_active_idx ON list_runs (list_id)
    WHERE status IN ('queued', 'running') AND kind IN ('build', 'refresh', 'enrich');

-- Where each lead came from: the provider and its id for the person. Kept
-- for compliance (a person can ask where their data came from) and so a
-- refresh never pays twice for someone already found.
CREATE TABLE lead_sources (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    lead_id        uuid NOT NULL REFERENCES leads (id) ON DELETE CASCADE,
    provider       text NOT NULL,
    external_id    text NOT NULL DEFAULT '',
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    list_id        uuid REFERENCES lead_lists (id) ON DELETE SET NULL,
    run_id         uuid REFERENCES list_runs (id) ON DELETE SET NULL,
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (lead_id, provider)
);

CREATE INDEX lead_sources_external_idx ON lead_sources (workspace_id, provider, external_id) WHERE external_id <> '';

-- Provider answers, so a retried run or a second list never pays for the
-- same lookup twice. Per workspace, from the workspace's own accounts,
-- and expired after max age (see the lists package).
CREATE TABLE enrichment_cache (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    provider     text NOT NULL,
    lookup_key   text NOT NULL,
    found        boolean NOT NULL,
    person       jsonb NOT NULL DEFAULT '{}',
    fetched_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, provider, lookup_key)
);
