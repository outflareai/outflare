-- Email a member sends from their own mailbox through Outflare (ADR 15):
-- queued after the policy check, sent by a job, and kept on the record
-- like captured mail. The content is sealed like captured mail; the
-- Message-ID finds it in the person's Sent mail, so a retry never sends
-- it twice.
CREATE TABLE crm_mail_sends (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    record_id uuid REFERENCES records (id) ON DELETE SET NULL,
    content_sealed bytea NOT NULL,
    message_id text NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'sent', 'failed')),
    error_code text,
    provider_id text,
    crm_mail_message_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz
);
CREATE INDEX crm_mail_sends_owner_idx ON crm_mail_sends (workspace_id, brand_id, owner_id, created_at DESC);
CREATE INDEX crm_mail_sends_record_idx ON crm_mail_sends (record_id) WHERE record_id IS NOT NULL;
