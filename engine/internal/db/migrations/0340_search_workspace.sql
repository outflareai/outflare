-- Work and report definitions belong to the site's brand. Evidence is retained
-- so editing filters later never rewrites what a person originally acted on.
CREATE TABLE search_insight_items (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE, site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 report text NOT NULL, key text NOT NULL, page text NOT NULL DEFAULT '', query text NOT NULL DEFAULT '',
 title text NOT NULL, recommendation text NOT NULL, evidence jsonb NOT NULL,
 tags text[] NOT NULL DEFAULT '{}', status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','in_progress','done','dismissed')),
 note text NOT NULL DEFAULT '', fix_id uuid REFERENCES fixes(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(site_id,report,key)
);
CREATE INDEX search_insight_items_brand ON search_insight_items(workspace_id,brand_id,site_id,status);
CREATE TABLE search_insight_reports (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 name text NOT NULL,tags text[] NOT NULL DEFAULT '{}',filters jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE search_report_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 report_id uuid NOT NULL REFERENCES search_insight_reports(id) ON DELETE CASCADE,
 status text NOT NULL DEFAULT 'running', error text NOT NULL DEFAULT '', filters jsonb NOT NULL,result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),completed_at timestamptz
);
CREATE INDEX search_report_runs_recent ON search_report_runs(workspace_id,brand_id,site_id,created_at DESC);
CREATE TABLE search_insight_schedules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 name text NOT NULL,integration_id uuid REFERENCES integrations(id) ON DELETE CASCADE,page text NOT NULL DEFAULT '',device text NOT NULL DEFAULT 'PHONE',
 frequency text NOT NULL CHECK(frequency IN ('off','daily','weekly')),weekday int NOT NULL CHECK(weekday BETWEEN 0 AND 6),
 hour int NOT NULL CHECK(hour BETWEEN 0 AND 23),timezone text NOT NULL,revision bigint NOT NULL DEFAULT 1,next_run_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX search_report_runs_definition ON search_report_runs(report_id,created_at DESC,id DESC);
