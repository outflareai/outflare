-- Smart list rules written from a description in plain English ("people
-- who paid in the last 90 days but never opened the app"): the
-- description, and the rules the workspace's own AI model wrote for it,
-- checked like a saved list's. A job writes them, so the row carries its
-- status; nothing is saved as a list until a person saves it.
CREATE TABLE smart_list_drafts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    -- person, or account for a list of companies.
    record_type  text NOT NULL DEFAULT 'person',
    description  text NOT NULL,
    -- queued, running, ready or failed. Only Outflare writes it, so it
    -- carries no check.
    status       text NOT NULL DEFAULT 'queued',
    -- Why it failed, in words a person can act on.
    error        text NOT NULL DEFAULT '',
    -- The answer: rules and match (empty when the description can't be
    -- written as rules), a name for the list, and one or two sentences on
    -- what the rules do or what is missing.
    rules        jsonb,
    match        text NOT NULL DEFAULT '',
    name         text NOT NULL DEFAULT '',
    explanation  text NOT NULL DEFAULT '',
    model        text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX smart_list_drafts_brand_idx ON smart_list_drafts (brand_id, created_at DESC);
CREATE INDEX smart_list_drafts_workspace_idx ON smart_list_drafts (workspace_id);

-- A date or time written in a record's field or an event's property, or
-- NULL when it isn't one, so a smart list rule such as "signed up in the
-- last 30 days" skips values that aren't dates instead of failing.
CREATE FUNCTION outflare_try_timestamptz(v text) RETURNS timestamptz
LANGUAGE plpgsql STABLE PARALLEL SAFE AS $$
BEGIN
    IF v IS NULL OR v !~ '^\s*\d{4}-\d{2}-\d{2}' THEN
        RETURN NULL;
    END IF;
    RETURN v::timestamptz;
EXCEPTION WHEN others THEN
    RETURN NULL;
END;
$$;
