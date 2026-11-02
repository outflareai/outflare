-- CRM work follows the customer record when records merge.
CREATE TABLE crm_activities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('task', 'note')),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    body text NOT NULL DEFAULT '' CHECK (length(body) <= 20000),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done')),
    priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high')),
    assigned_to uuid REFERENCES users(id) ON DELETE SET NULL,
    due_at timestamptz,
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_activities_brand_idx ON crm_activities(workspace_id, brand_id, created_at DESC, id DESC);
CREATE INDEX crm_activities_record_idx ON crm_activities(workspace_id, record_id, created_at DESC, id DESC);
CREATE INDEX crm_activities_work_idx ON crm_activities(workspace_id, assigned_to, status, due_at) WHERE kind = 'task';

CREATE TABLE crm_views (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    record_type text NOT NULL,
    shared boolean NOT NULL DEFAULT false,
    config jsonb NOT NULL DEFAULT '{}',
    created_by uuid REFERENCES users(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX crm_views_brand_idx ON crm_views(workspace_id, brand_id, record_type);

-- Tasks and notes stay with their record when its owning brand changes.
CREATE FUNCTION outflare_crm_record_brand() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id THEN
        UPDATE crm_activities SET brand_id = NEW.brand_id, version = version + 1, updated_at = now()
        WHERE workspace_id = NEW.workspace_id AND record_id = NEW.id;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER crm_record_brand AFTER UPDATE OF brand_id ON records
    FOR EACH ROW EXECUTE FUNCTION outflare_crm_record_brand();

ALTER TABLE crm_activities ADD COLUMN integration_id uuid REFERENCES integrations(id) ON DELETE SET NULL;
ALTER TABLE crm_activities ADD COLUMN source_id text;
CREATE UNIQUE INDEX crm_activities_source_idx ON crm_activities(workspace_id,integration_id,source_id,record_id) WHERE source_id IS NOT NULL;

CREATE INDEX records_crm_name_idx ON records(workspace_id,brand_id,lower(name),id)
    WHERE deleted_at IS NULL AND merged_into IS NULL AND archived_at IS NULL;
CREATE INDEX records_crm_updated_idx ON records(workspace_id,brand_id,updated_at DESC,id DESC)
    WHERE deleted_at IS NULL AND merged_into IS NULL AND archived_at IS NULL;
