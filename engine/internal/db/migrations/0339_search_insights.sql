-- Additive search analysis storage. Segments remain separate from page/query
-- totals: summing the two would double count and hide anonymisation differences.
CREATE TABLE search_segments (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid REFERENCES brands(id) ON DELETE CASCADE,
 integration_id uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
 site text NOT NULL, host text NOT NULL, day date NOT NULL,
 page text NOT NULL, country text NOT NULL, device text NOT NULL,
 clicks integer NOT NULL, impressions integer NOT NULL, position double precision NOT NULL,
 PRIMARY KEY(integration_id,site,day,page,country,device)
);
CREATE INDEX search_segments_ws ON search_segments(workspace_id,host,day);
CREATE TABLE search_completeness (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid REFERENCES brands(id) ON DELETE CASCADE,
 integration_id uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
 site text NOT NULL, day date NOT NULL, clicks double precision NOT NULL,
 impressions double precision NOT NULL, visible_clicks double precision NOT NULL,
 visible_impressions double precision NOT NULL, rows_read integer NOT NULL,
 row_limit integer NOT NULL, capped boolean NOT NULL,
 PRIMARY KEY(integration_id,site,day)
);
CREATE TABLE search_groups (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 name text NOT NULL, kind text NOT NULL CHECK(kind IN ('content','topic')),
 rules jsonb NOT NULL, UNIQUE(site_id,name)
);
CREATE TABLE search_migrations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 target_site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 name text NOT NULL, go_live date NOT NULL, mappings jsonb NOT NULL,
 UNIQUE(site_id,name)
);
CREATE TABLE search_vitals (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 page text NOT NULL, device text NOT NULL, day date NOT NULL,
 metric text NOT NULL, p75 double precision NOT NULL, good_share double precision NOT NULL,
 PRIMARY KEY(site_id,page,device,day,metric)
);
CREATE TABLE search_algorithm_updates (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 site_id uuid NOT NULL REFERENCES seo_sites(id) ON DELETE CASCADE,
 id text NOT NULL, name text NOT NULL, started_at date NOT NULL, ended_at date,
 PRIMARY KEY(site_id,id)
);
CREATE TABLE search_ga4_outcomes (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid REFERENCES brands(id) ON DELETE CASCADE,
 integration_id uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
 site text NOT NULL, day date NOT NULL, host text NOT NULL, path text NOT NULL,
 source text NOT NULL, sessions integer NOT NULL, conversions integer NOT NULL,
 revenue_micros bigint NOT NULL, currency text NOT NULL,
 PRIMARY KEY(integration_id,site,day,host,path,source)
);
CREATE INDEX search_ga4_outcomes_ws ON search_ga4_outcomes(workspace_id,host,day);
