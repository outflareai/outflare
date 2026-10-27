-- Agencies: one team running outbound for many clients. Each client is an
-- ordinary workspace with its own mailboxes, leads, sequences and members;
-- the agency's people and tokens reach every client workspace without being
-- added to each one.

CREATE TABLE agencies (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name          text NOT NULL,
    -- White label: what client members see instead of Outflare.
    brand_name    text NOT NULL DEFAULT '',
    logo_url      text NOT NULL DEFAULT '',
    accent_color  text NOT NULL DEFAULT '',
    support_email text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- An agency member has this role in every client workspace, or a higher
-- role where they are also a direct member.
CREATE TABLE agency_members (
    agency_id  uuid NOT NULL REFERENCES agencies (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('owner', 'admin', 'operator', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agency_id, user_id)
);

CREATE INDEX agency_members_user_idx ON agency_members (user_id);

ALTER TABLE workspaces ADD COLUMN agency_id uuid REFERENCES agencies (id) ON DELETE SET NULL;
CREATE INDEX workspaces_agency_idx ON workspaces (agency_id);

-- Agency tokens work in every client workspace, so one agent can run all
-- of an agency's clients. Scopes apply in each workspace.
CREATE TABLE agency_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agency_id    uuid NOT NULL REFERENCES agencies (id) ON DELETE CASCADE,
    actor_id     uuid NOT NULL UNIQUE REFERENCES actors (id),
    name         text NOT NULL,
    prefix       text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,
    scopes       text[] NOT NULL,
    -- May it create new client workspaces?
    can_create_clients boolean NOT NULL DEFAULT false,
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    last_used_at timestamptz,
    revoked_at   timestamptz
);

-- Agency-level changes (clients added, members, tokens, branding).
CREATE TABLE agency_events (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agency_id  uuid NOT NULL REFERENCES agencies (id) ON DELETE CASCADE,
    actor_id   uuid REFERENCES actors (id),
    action     text NOT NULL,
    target     text NOT NULL DEFAULT '',
    data       jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX agency_events_idx ON agency_events (agency_id, created_at DESC);
