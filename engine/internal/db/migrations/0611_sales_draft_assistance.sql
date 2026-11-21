-- Personal AI previews are separate from saved replies: generating never
-- overwrites a draft or sends an email. Inputs and outputs stay encrypted.
CREATE TABLE sales_draft_assists (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 lead_id uuid NOT NULL REFERENCES sales_leads(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 base_revision integer NOT NULL CHECK(base_revision>=0),
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','ready','failed')),
 input_sealed bytea NOT NULL,
 output_sealed bytea,
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sales_draft_assists_owner ON sales_draft_assists(owner_id);
CREATE INDEX sales_draft_assists_brand ON sales_draft_assists(brand_id);
CREATE INDEX sales_draft_assists_lead_fk ON sales_draft_assists(lead_id);
CREATE INDEX sales_draft_assists_attention ON sales_draft_assists(status,updated_at) WHERE status IN ('queued','running');
CREATE INDEX sales_draft_assists_record ON sales_draft_assists(record_id);
CREATE INDEX sales_draft_assists_lead ON sales_draft_assists(workspace_id,brand_id,lead_id,owner_id,created_at DESC);
CREATE UNIQUE INDEX sales_draft_assists_pending ON sales_draft_assists(workspace_id,brand_id,lead_id,owner_id) WHERE status IN ('queued','running');

CREATE FUNCTION outflare_sales_assist_record_brand() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id THEN
  UPDATE sales_draft_assists SET brand_id=NEW.brand_id WHERE workspace_id=NEW.workspace_id AND record_id=NEW.id;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER sales_assist_record_brand AFTER UPDATE OF brand_id ON records FOR EACH ROW EXECUTE FUNCTION outflare_sales_assist_record_brand();

-- Feedback is a small subset of the append-only event history.
CREATE INDEX sales_events_feedback ON sales_events(workspace_id,brand_id,lead_id,occurred_at DESC,id DESC) WHERE action='feedback_added';
