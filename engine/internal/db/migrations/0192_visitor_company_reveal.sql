-- Company reveal: the company behind a website visitor's IP address, looked
-- up with the workspace's own provider (People Data Labs, Snitcher). The
-- address itself is never stored: lookups are kept by a keyed hash, with
-- the company's domain and name, so one address costs one lookup a week.

ALTER TABLE visitor_settings
    ADD COLUMN reveal_companies boolean NOT NULL DEFAULT false,
    -- Which connection looks addresses up; null means the first one that can.
    ADD COLUMN reveal_integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- Lookups a day at most, so the provider's credits are never run down.
    ADD COLUMN reveal_daily_limit integer NOT NULL DEFAULT 500 CHECK (reveal_daily_limit BETWEEN 1 AND 100000),
    -- Domains the workspace marked as not a company (an ISP, a carrier, a
    -- shared network): lookups that name them name nobody.
    ADD COLUMN not_companies text[] NOT NULL DEFAULT '{}';

-- When the browser's address was last sent for a lookup, and the kind of
-- network it came from (business, isp, mobile, hosting), so visitors on a
-- home or phone connection are shown as such and never as a company.
-- A company a lookup named stays on the visitor as a domain and name until
-- someone adds it to the CRM (or the CRM has it already): lookups are often
-- wrong, so they never make records on their own.
ALTER TABLE site_visitors
    ADD COLUMN reveal_checked_at timestamptz,
    ADD COLUMN network text NOT NULL DEFAULT '',
    ADD COLUMN company_domain text NOT NULL DEFAULT '',
    ADD COLUMN company_name text NOT NULL DEFAULT '';

CREATE INDEX site_visitors_company_domain_idx ON site_visitors (workspace_id, company_domain) WHERE company_domain <> '';

CREATE TABLE visitor_ip_lookups (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- HMAC of the address with the server's key: not reversible without it.
    ip_hash        text NOT NULL,
    found          boolean NOT NULL,
    -- business, isp, mobile, hosting or '' (unknown).
    network        text NOT NULL DEFAULT '',
    domain         text NOT NULL DEFAULT '',
    name           text NOT NULL DEFAULT '',
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    checked_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, ip_hash)
);

CREATE INDEX visitor_ip_lookups_day_idx ON visitor_ip_lookups (workspace_id, checked_at);
