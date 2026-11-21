-- Partnerships: a brand's saved offer templates, and its rule for a paid link
-- that is removed after payment. Both belong to one brand.
CREATE TABLE partner_offer_templates (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
 config jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_offer_templates_brand_idx ON partner_offer_templates(workspace_id, brand_id, name);

-- removal: restore_or_refund (restore within restore_days or refund, for
-- guarantee_months), restore_or_refund_forever, shrinking_refund (the refund
-- falls evenly to nothing over guarantee_months) or warn_only.
CREATE TABLE partner_link_rules (
 brand_id uuid PRIMARY KEY REFERENCES brands(id) ON DELETE CASCADE,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 removal text NOT NULL DEFAULT 'restore_or_refund' CHECK (removal IN ('restore_or_refund','restore_or_refund_forever','shrinking_refund','warn_only')),
 restore_days integer NOT NULL DEFAULT 14 CHECK (restore_days BETWEEN 1 AND 90),
 guarantee_months integer NOT NULL DEFAULT 12 CHECK (guarantee_months BETWEEN 1 AND 120),
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- Both in Query (AGENTS.md, "Data: agents can see all of it"), with the
-- scope the API asks for the same data (partners.read).
CREATE VIEW query.partner_offer_templates WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.config, x.created_at, x.updated_at
FROM partner_offer_templates x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.partner_link_rules WITH (security_barrier) AS
SELECT x.brand_id, x.removal, x.restore_days, x.guarantee_months, x.updated_at
FROM partner_link_rules x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'partners.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
