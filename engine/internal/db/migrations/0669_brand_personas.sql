-- Personas: the kinds of people a brand sells to, each with what they
-- want, what hurts, what makes them start looking and what holds them
-- back. They are types, never real people: a real person or company lives
-- in customer records. Like profile facts, one an agent wrote stays
-- "suggested" until a person confirms it.
CREATE TABLE brand_personas (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- Who they are, in a line: their job and the kind of company.
    role         text NOT NULL DEFAULT '' CHECK (length(role) <= 200),
    summary      text NOT NULL DEFAULT '' CHECK (length(summary) <= 4000),
    -- Lists of short sentences.
    goals        jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(goals) = 'array'),
    pains        jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(pains) = 'array'),
    triggers     jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(triggers) = 'array'),
    objections   jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(objections) = 'array'),
    channels     jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(channels) = 'array'),
    source       text NOT NULL DEFAULT 'person' CHECK (source IN ('person', 'interview', 'website',
                     'conversation', 'agent', 'import')),
    status       text NOT NULL DEFAULT 'confirmed' CHECK (status IN ('suggested', 'confirmed', 'archived')),
    confirmed_by uuid REFERENCES actors (id) ON DELETE SET NULL,
    confirmed_at timestamptz,
    position     integer NOT NULL DEFAULT 0,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX brand_personas_name_idx ON brand_personas (brand_id, lower(name));
CREATE INDEX brand_personas_workspace_idx ON brand_personas (workspace_id, created_at DESC);
