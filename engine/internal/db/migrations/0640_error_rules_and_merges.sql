-- Error tracking, second round (docs/error-tracking.md).

-- Ignore rules: errors a brand never wants in its open list, matched by a
-- piece of text in one part of the error. Errors that match stay stored
-- and show under Filtered, so a rule can be taken back; they never alert.
CREATE TABLE error_rules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- Where to look: the message, the type, the files in the stack, the
    -- page's address or its site (host).
    field        text NOT NULL CHECK (field IN ('message', 'type', 'stack', 'page', 'host')),
    -- The text to find, case not mattering.
    pattern      text NOT NULL CHECK (length(pattern) BETWEEN 1 AND 500),
    -- Why, in the words of whoever made it.
    note         text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, field, pattern)
);
CREATE INDEX error_rules_workspace_idx ON error_rules (workspace_id);

-- Issues put together: the errors of fingerprint count as into's. A
-- person merges by hand; the regroup job also records each issue the old
-- grouping made, so links to it still open the issue it is now part of.
CREATE TABLE error_issue_merges (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- Both are checked by the code that writes them (lowercase letters and
    -- digits, never the same), not here, so the version before this one
    -- still runs on this schema.
    fingerprint  text NOT NULL,
    into_fingerprint text NOT NULL,
    -- hand: a person merged it; regroup: the new grouping did.
    kind         text NOT NULL CHECK (kind IN ('hand', 'regroup')),
    merged_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    merged_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, fingerprint)
);
CREATE INDEX error_issue_merges_workspace_idx ON error_issue_merges (workspace_id);
CREATE INDEX error_issue_merges_into_idx ON error_issue_merges (brand_id, into_fingerprint);

-- Errors are now grouped on the server (the group property), whatever
-- the browser or the build. Errors already stored get it from a job, once
-- per workspace that has any.
INSERT INTO jobs (workspace_id, kind, payload, max_attempts, idempotency_key)
SELECT w.id, 'errors.regroup', '{}'::jsonb, 10, 'errors.regroup:0640:' || w.id
FROM workspaces w
WHERE EXISTS (SELECT 1 FROM customer_events e WHERE e.workspace_id = w.id AND e.name = 'error_caught' AND e.occurred_at > now() - interval '90 days');
