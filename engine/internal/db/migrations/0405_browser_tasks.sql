-- Browser tasks (plans/brand-agent, "Browser automation"): bounded work a
-- browser worker does for a brand on sites with no API. A task is a
-- capability: the account it may sign in as, the hosts it may reach, what
-- it may do, the approved content it may type, and ceilings on steps,
-- time and AI spend. Its steps are kept as a log (a secret's kind, never
-- its value); a task that meets MFA, a CAPTCHA, a passkey or a consent
-- screen waits on a takeover and runs again when a person hands it back.
CREATE TABLE browser_tasks (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    account_id       uuid REFERENCES brand_accounts (id) ON DELETE SET NULL,
    goal             text NOT NULL CHECK (length(goal) BETWEEN 1 AND 4000),
    -- The hosts the browser may reach (each with its subdomains).
    hosts            text[] NOT NULL,
    -- read, act (click, type approved content, submit), sign_in.
    operations       text[] NOT NULL,
    -- Approved texts by name, typed exactly as given.
    content          jsonb NOT NULL DEFAULT '{}',
    max_steps        integer NOT NULL DEFAULT 40 CHECK (max_steps BETWEEN 1 AND 200),
    max_minutes      integer NOT NULL DEFAULT 10 CHECK (max_minutes BETWEEN 1 AND 60),
    max_cents        bigint NOT NULL DEFAULT 100 CHECK (max_cents BETWEEN 1 AND 10000),
    status           text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'waiting_person', 'done', 'failed', 'cancelled')),
    summary          text NOT NULL DEFAULT '',
    final_url        text NOT NULL DEFAULT '',
    evidence         jsonb NOT NULL DEFAULT '{}',
    takeover_id      uuid REFERENCES brand_account_takeovers (id) ON DELETE SET NULL,
    -- resumed: a person handed the account back since the last run.
    resumed          boolean NOT NULL DEFAULT false,
    cancel_requested boolean NOT NULL DEFAULT false,
    runs             integer NOT NULL DEFAULT 0,
    steps            integer NOT NULL DEFAULT 0,
    ai_cents         bigint NOT NULL DEFAULT 0,
    created_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    finished_at      timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX browser_tasks_brand_idx ON browser_tasks (brand_id, status, created_at DESC);
CREATE INDEX browser_tasks_takeover_idx ON browser_tasks (takeover_id) WHERE takeover_id IS NOT NULL;

CREATE TABLE browser_task_steps (
    task_id      uuid NOT NULL REFERENCES browser_tasks (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    run          integer NOT NULL,
    n            integer NOT NULL,
    tool         text NOT NULL,
    -- What the tool was given: a ref, a URL, a content name, a secret's
    -- kind; never a secret's value.
    input        jsonb NOT NULL DEFAULT '{}',
    result       text NOT NULL DEFAULT '',
    at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, run, n)
);
