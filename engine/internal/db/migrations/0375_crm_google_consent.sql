CREATE TABLE crm_mail_rollout (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid PRIMARY KEY REFERENCES brands(id) ON DELETE CASCADE,
 config jsonb NOT NULL DEFAULT '{"enabled":false,"allowed_user_ids":[],"excluded_user_ids":[]}',
 version integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE crm_mail_oauth_flows (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 state_hash text NOT NULL UNIQUE,
 verifier_sealed bytea NOT NULL,
 nonce text NOT NULL,
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE crm_google_connections (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 credentials_sealed bytea NOT NULL,
 status text NOT NULL DEFAULT 'connected' CHECK(status IN('connected','reconnect_required')),
 version integer NOT NULL DEFAULT 1,
 last_synced_at timestamptz,
 error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(brand_id,owner_id)
);
CREATE TABLE crm_mail_capture_targets (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 enabled boolean NOT NULL DEFAULT true,
 version integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(owner_id,record_id)
);
CREATE INDEX crm_google_connections_brand_idx ON crm_google_connections(workspace_id,brand_id);
CREATE INDEX crm_mail_flows_expiry_idx ON crm_mail_oauth_flows(expires_at);
-- Content-free source tombstones prevent a deleted provider copy from
-- reappearing on the next overlapping sync. No customer identity is retained.
CREATE TABLE crm_mail_suppressions (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 source_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(brand_id,owner_id,source_hash)
);
-- Membership removal prunes stale enrollment and destroys personal grants.
-- Update rollout rows before deleting grants to preserve the same lock order
-- as administrative rollout changes. No email content is read by this hook.
CREATE FUNCTION outflare_crm_mail_member_removed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE crm_mail_rollout SET
  config=jsonb_set(jsonb_set(config,'{allowed_user_ids}',COALESCE(config->'allowed_user_ids','[]'::jsonb)-OLD.user_id::text),'{excluded_user_ids}',COALESCE(config->'excluded_user_ids','[]'::jsonb)-OLD.user_id::text),
  version=version+1,updated_at=now()
 WHERE workspace_id=OLD.workspace_id AND ((config->'allowed_user_ids')?OLD.user_id::text OR (config->'excluded_user_ids')?OLD.user_id::text);
 DELETE FROM crm_google_connections WHERE workspace_id=OLD.workspace_id AND owner_id=OLD.user_id;
 DELETE FROM crm_mail_oauth_flows WHERE workspace_id=OLD.workspace_id AND owner_id=OLD.user_id;
 RETURN OLD;
END;
$$;
CREATE TRIGGER crm_mail_member_removed AFTER DELETE ON memberships FOR EACH ROW EXECUTE FUNCTION outflare_crm_mail_member_removed();
