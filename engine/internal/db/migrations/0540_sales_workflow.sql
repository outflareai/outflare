-- Sales is a workflow over CRM records. It does not own a second directory.
-- Message, draft, template and note content uses the engine's sealed storage.
CREATE TABLE sales_members (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 role text NOT NULL CHECK(role IN ('sdr','ae','manager','none')),
 introduction text NOT NULL DEFAULT '' CHECK(length(introduction)<=1000),
 queue_seen_at timestamptz NOT NULL DEFAULT '-infinity',
 PRIMARY KEY(workspace_id,brand_id,user_id)
);
CREATE TABLE sales_mail_connections (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 credentials_sealed bytea NOT NULL,
 status text NOT NULL DEFAULT 'connected' CHECK(status IN ('connected','reconnect_required')),
 last_synced_at timestamptz,
 error_code text,
 generation integer NOT NULL DEFAULT 1,
 UNIQUE(workspace_id,brand_id,owner_id)
);
CREATE TABLE sales_oauth_flows (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 state_hash text NOT NULL UNIQUE,
 verifier_sealed bytea NOT NULL,
 nonce text NOT NULL,
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz
);
CREATE TABLE sales_leads (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 source text NOT NULL CHECK(length(source) BETWEEN 1 AND 100),
 source_id text NOT NULL CHECK(length(source_id) BETWEEN 1 AND 200),
 integration_id uuid REFERENCES integrations(id) ON DELETE SET NULL,
 source_sealed bytea NOT NULL,
 state text NOT NULL DEFAULT 'new' CHECK(state IN ('new','active','customer','archived')),
 owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
 sdr_id uuid REFERENCES users(id) ON DELETE SET NULL,
 ae_id uuid REFERENCES users(id) ON DELETE SET NULL,
 details_sealed bytea,
 amount_minor bigint CHECK(amount_minor>=0),
 currency text NOT NULL DEFAULT 'USD' CHECK(currency ~ '^[A-Z]{3}$'),
 introduced_at timestamptz,
 introduced_by uuid REFERENCES users(id) ON DELETE SET NULL,
 confirmed_at timestamptz,
 confirmed_by uuid REFERENCES users(id) ON DELETE SET NULL,
 reply_state text NOT NULL DEFAULT 'reply_received',
 version integer NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,brand_id,source,source_id)
);
CREATE INDEX sales_leads_queue ON sales_leads(workspace_id,brand_id,state,updated_at DESC,id DESC);
CREATE INDEX sales_leads_record ON sales_leads(record_id);
CREATE INDEX sales_leads_owner ON sales_leads(workspace_id,brand_id,owner_id,state);
CREATE TABLE sales_templates (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 version integer NOT NULL DEFAULT 1,
 archived boolean NOT NULL DEFAULT false
);
CREATE INDEX sales_templates_owner ON sales_templates(workspace_id,brand_id,owner_id);
CREATE TABLE sales_template_versions (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 template_id uuid NOT NULL REFERENCES sales_templates(id) ON DELETE CASCADE,
 version integer NOT NULL,
 content_sealed bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(template_id,version)
);
CREATE TABLE sales_drafts (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 lead_id uuid NOT NULL REFERENCES sales_leads(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 revision integer NOT NULL,
 template_version_id uuid REFERENCES sales_template_versions(id) ON DELETE SET NULL,
 content_sealed bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(lead_id,owner_id,revision)
);
CREATE INDEX sales_drafts_record ON sales_drafts(record_id);
CREATE TABLE sales_messages (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 lead_id uuid NOT NULL REFERENCES sales_leads(id) ON DELETE CASCADE,
 sender_id uuid REFERENCES users(id) ON DELETE SET NULL,
 handoff_to uuid REFERENCES users(id) ON DELETE SET NULL,
 draft_id uuid UNIQUE REFERENCES sales_drafts(id) ON DELETE SET NULL,
 superseded_by uuid REFERENCES sales_messages(id) ON DELETE SET NULL,
 kind text NOT NULL DEFAULT 'reply' CHECK(kind IN ('reply','introduction','handoff')),
 direction text NOT NULL CHECK(direction IN ('incoming','outgoing')),
 status text NOT NULL CHECK(status IN ('received','queued','sending','accepted','failed','uncertain')),
 content_sealed bytea NOT NULL,
 identity_hash text NOT NULL,
 occurred_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,brand_id,lead_id,identity_hash)
);
CREATE INDEX sales_messages_record ON sales_messages(record_id);
CREATE INDEX sales_messages_superseded ON sales_messages(superseded_by) WHERE superseded_by IS NOT NULL;
CREATE INDEX sales_messages_lead ON sales_messages(workspace_id,brand_id,lead_id,occurred_at,id);
CREATE INDEX sales_messages_attention ON sales_messages(status,occurred_at) WHERE status IN ('queued','sending','failed','uncertain');
CREATE TABLE sales_message_copies (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 message_id uuid NOT NULL REFERENCES sales_messages(id) ON DELETE CASCADE,
 provider_hash text NOT NULL,
 content_sealed bytea NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,brand_id,provider_hash)
);
CREATE INDEX sales_message_copies_message ON sales_message_copies(message_id);
CREATE TABLE sales_events (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 lead_id uuid NOT NULL REFERENCES sales_leads(id) ON DELETE CASCADE,
 actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
 actor_role text NOT NULL,
 action text NOT NULL,
 schema_version integer NOT NULL DEFAULT 1,
 content_sealed bytea NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sales_events_record ON sales_events(record_id);
CREATE INDEX sales_events_lead ON sales_events(workspace_id,brand_id,lead_id,occurred_at,id);
CREATE TABLE sales_seen (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 lead_id uuid NOT NULL REFERENCES sales_leads(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 seen_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,brand_id,lead_id,user_id)
);
-- A CRM brand move transfers workflow and protected evidence together.
CREATE FUNCTION outflare_sales_record_brand() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id THEN
  UPDATE sales_leads SET brand_id=NEW.brand_id WHERE workspace_id=NEW.workspace_id AND record_id=NEW.id;
  UPDATE sales_drafts SET brand_id=NEW.brand_id WHERE workspace_id=NEW.workspace_id AND record_id=NEW.id;
  UPDATE sales_messages SET brand_id=NEW.brand_id WHERE workspace_id=NEW.workspace_id AND record_id=NEW.id;
  UPDATE sales_events SET brand_id=NEW.brand_id WHERE workspace_id=NEW.workspace_id AND record_id=NEW.id;
  UPDATE sales_message_copies c SET brand_id=NEW.brand_id FROM sales_messages m WHERE c.workspace_id=NEW.workspace_id AND m.workspace_id=NEW.workspace_id AND c.message_id=m.id AND m.record_id=NEW.id;
  UPDATE sales_seen s SET brand_id=NEW.brand_id FROM sales_leads l WHERE s.workspace_id=NEW.workspace_id AND l.workspace_id=NEW.workspace_id AND s.lead_id=l.id AND l.record_id=NEW.id;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER sales_record_brand AFTER UPDATE OF brand_id ON records FOR EACH ROW EXECUTE FUNCTION outflare_sales_record_brand();

-- Removing membership or brand access revokes the corresponding consent.
-- Conversation evidence remains owned by the CRM record.
CREATE FUNCTION outflare_sales_membership_removed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  DELETE FROM sales_mail_connections WHERE workspace_id=OLD.workspace_id AND owner_id=OLD.user_id;
  DELETE FROM sales_oauth_flows WHERE workspace_id=OLD.workspace_id AND owner_id=OLD.user_id;
  DELETE FROM sales_members WHERE workspace_id=OLD.workspace_id AND user_id=OLD.user_id;
 ELSE
  IF cardinality(NEW.brand_ids)>0 THEN
   DELETE FROM sales_mail_connections WHERE workspace_id=NEW.workspace_id AND owner_id=NEW.user_id AND NOT(brand_id=ANY(NEW.brand_ids));
   DELETE FROM sales_oauth_flows WHERE workspace_id=NEW.workspace_id AND owner_id=NEW.user_id AND NOT(brand_id=ANY(NEW.brand_ids));
   DELETE FROM sales_members WHERE workspace_id=NEW.workspace_id AND user_id=NEW.user_id AND NOT(brand_id=ANY(NEW.brand_ids));
  END IF;
 END IF;
 RETURN NULL;
END;
$$;
CREATE TRIGGER sales_membership_removed AFTER DELETE OR UPDATE OF brand_ids ON memberships FOR EACH ROW EXECUTE FUNCTION outflare_sales_membership_removed();
