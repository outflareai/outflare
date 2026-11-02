-- Personal CRM mail is isolated from the shared outreach inbox. No role
-- (including owner/admin) implicitly grants access to another user's mail.
CREATE TABLE crm_mail_policy (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid PRIMARY KEY REFERENCES brands(id) ON DELETE CASCADE,
 config jsonb NOT NULL DEFAULT '{"internal_domains":[],"blocked_addresses":[],"blocked_domains":[]}',
 version integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE crm_mail_preferences (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 config jsonb NOT NULL DEFAULT '{"blocked_addresses":[],"blocked_domains":[]}',
 version integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(brand_id,owner_id)
);
CREATE TABLE crm_mail_sharing (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 config jsonb NOT NULL DEFAULT '{"visibility":"private","audience":"selected","viewer_ids":[]}',
 version integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_id,record_id)
);
CREATE TABLE crm_mail_messages (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 -- All headers, participants, subject, body and source identifiers are sealed.
 content_sealed bytea NOT NULL,
 source_hash text NOT NULL,
 occurred_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(brand_id,owner_id,source_hash)
);
CREATE INDEX crm_mail_messages_record_idx ON crm_mail_messages(workspace_id,brand_id,record_id,id DESC);
