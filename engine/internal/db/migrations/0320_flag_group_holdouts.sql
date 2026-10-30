-- An exclusion group's holdout: the top holdout percent of the group's
-- buckets is kept out of every flag in it, so those visitors see the default
-- experience everywhere the group's tests run, and the group's combined
-- effect can be measured against them. A group without a row has none.
CREATE TABLE flag_groups (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL,
    holdout      int NOT NULL DEFAULT 0 CHECK (holdout BETWEEN 0 AND 50),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, name)
);
