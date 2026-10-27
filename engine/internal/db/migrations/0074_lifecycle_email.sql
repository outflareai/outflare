-- Lifecycle and transactional email: mail to people who asked for it
-- (receipts, password resets, sign-in codes, onboarding), sent through the
-- workspace's own email provider account (Amazon SES, Postmark, Resend).
-- Cold outreach never goes this way: it stays on mailboxes.

-- An email provider is a connected integration that sends. One row per
-- integration, made when the integration is connected.
CREATE TABLE email_providers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid REFERENCES brands (id) ON DELETE SET NULL,
    integration_id uuid NOT NULL UNIQUE REFERENCES integrations (id) ON DELETE CASCADE,
    connector      text NOT NULL,
    -- Every send goes through a channel. Email is the only one today; SMS
    -- and push providers join as rows with their own channel.
    channel        text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'sms', 'push', 'whatsapp')),
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    -- What it may send. A send picks the default provider for its purpose.
    purposes       text[] NOT NULL DEFAULT '{transactional,lifecycle}',
    is_default     boolean NOT NULL DEFAULT false,
    -- Delivery events (deliveries, bounces, complaints) coming back:
    -- not_set_up, setting_up, active, manual (a person must add the
    -- callback URL in the provider) or failed (events_error says why).
    events_status  text NOT NULL DEFAULT 'not_set_up'
                   CHECK (events_status IN ('not_set_up', 'setting_up', 'active', 'manual', 'failed')),
    events_error   text,
    last_event_at  timestamptz,
    -- Sealed JSON with the id: the callback URL's token and what the
    -- provider handed back (signing secret, configuration set).
    secrets        bytea NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_providers_workspace ON email_providers (workspace_id);
-- At most one default provider per channel in a workspace.
CREATE UNIQUE INDEX email_providers_default ON email_providers (workspace_id, channel) WHERE is_default;

-- A domain the workspace sends from, registered with one provider.
CREATE TABLE email_domains (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    provider_id  uuid NOT NULL REFERENCES email_providers (id) ON DELETE CASCADE,
    domain       text NOT NULL CHECK (domain = lower(domain)),
    -- pending until the provider verifies it; failed when it gave up.
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified', 'failed')),
    status_reason text,
    -- [{purpose, type, name, value, priority, required, status, found}]
    records      jsonb NOT NULL DEFAULT '[]',
    provider_ref text, -- the provider's id for the domain
    checked_at   timestamptz,
    verified_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, domain)
);
CREATE INDEX email_domains_workspace ON email_domains (workspace_id, domain);

-- Templates: the draft people edit, and immutable published versions that
-- sends use. A send can pin a version, so editing a receipt never changes
-- one already in use without a publish.
CREATE TABLE email_templates (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid REFERENCES brands (id) ON DELETE SET NULL,
    handle            text NOT NULL, -- what the API names it by: "password-reset"
    name              text NOT NULL,
    channel           text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'sms', 'push', 'whatsapp')),
    purpose           text NOT NULL DEFAULT 'transactional' CHECK (purpose IN ('transactional', 'lifecycle')),
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    from_address      text NOT NULL DEFAULT '', -- default sender, e.g. Acme <billing@acme.example>
    reply_to          text NOT NULL DEFAULT '',
    subject           text NOT NULL DEFAULT '',
    html              text NOT NULL DEFAULT '',
    text_body         text NOT NULL DEFAULT '',
    -- [{name, required, default, example}]
    variables         jsonb NOT NULL DEFAULT '[]',
    published_version integer,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, handle)
);

CREATE TABLE email_template_versions (
    template_id  uuid NOT NULL REFERENCES email_templates (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    version      integer NOT NULL CHECK (version > 0),
    from_address text NOT NULL DEFAULT '',
    reply_to     text NOT NULL DEFAULT '',
    subject      text NOT NULL,
    html         text NOT NULL,
    text_body    text NOT NULL,
    variables    jsonb NOT NULL DEFAULT '[]',
    note         text NOT NULL DEFAULT '',
    published_by uuid,
    published_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (template_id, version)
);

-- One row per email Outflare was asked to send, with its delivery state.
CREATE TABLE emails (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id            uuid REFERENCES brands (id) ON DELETE SET NULL,
    provider_id         uuid REFERENCES email_providers (id) ON DELETE SET NULL,
    channel             text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'sms', 'push', 'whatsapp')),
    purpose             text NOT NULL CHECK (purpose IN ('transactional', 'lifecycle')),
    from_address        text NOT NULL,
    to_addresses        text[] NOT NULL,
    cc                  text[] NOT NULL DEFAULT '{}',
    bcc                 text[] NOT NULL DEFAULT '{}',
    reply_to            text[] NOT NULL DEFAULT '{}',
    subject             text NOT NULL,
    html                text NOT NULL DEFAULT '',
    text_body           text NOT NULL DEFAULT '',
    template_id         uuid REFERENCES email_templates (id) ON DELETE SET NULL,
    template_version    integer,
    tags                text[] NOT NULL DEFAULT '{}',
    metadata            jsonb NOT NULL DEFAULT '{}',
    -- queued, scheduled (waiting for send_at), sending, sent (the provider
    -- took it), delivered (the receiving server took it), delayed,
    -- bounced, complained, suppressed (the policy check or the provider
    -- refused the address), failed, cancelled.
    status              text NOT NULL DEFAULT 'queued' CHECK (status IN
                        ('queued', 'scheduled', 'sending', 'sent', 'delivered', 'delayed', 'bounced',
                         'complained', 'suppressed', 'failed', 'cancelled')),
    status_reason       text,
    bounce_type         text CHECK (bounce_type IN ('hard', 'soft')),
    provider_message_id text,
    test                boolean NOT NULL DEFAULT false,
    send_at             timestamptz,
    sent_at             timestamptz,
    delivered_at        timestamptz,
    attempts            integer NOT NULL DEFAULT 0,
    created_by          uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX emails_workspace_created ON emails (workspace_id, created_at DESC, id DESC);
CREATE INDEX emails_provider_message ON emails (provider_id, provider_message_id) WHERE provider_message_id IS NOT NULL;
CREATE INDEX emails_template ON emails (template_id) WHERE template_id IS NOT NULL;

-- Each email's timeline: queued, sent, delivered, bounced and so on.
CREATE TABLE email_events (
    id                bigserial PRIMARY KEY,
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email_id          uuid NOT NULL REFERENCES emails (id) ON DELETE CASCADE,
    type              text NOT NULL,
    recipient         text,
    detail            text,
    -- The provider's id for the event, so a repeated callback is dropped.
    provider_event_id text,
    occurred_at       timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_events_email ON email_events (email_id, occurred_at);
CREATE UNIQUE INDEX email_events_dedupe ON email_events (email_id, provider_event_id) WHERE provider_event_id IS NOT NULL;
