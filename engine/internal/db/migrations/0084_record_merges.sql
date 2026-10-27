-- Duplicate profiles: pairs of records that may be the same human, for
-- someone to merge or dismiss, and the log of merges. Outflare never merges
-- on its own on anything weaker than a verified identity, and never merges
-- two different user ids (two real accounts in the workspace's product).

CREATE TABLE record_merge_candidates (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The pair, lower id first, so each pair is asked about once.
    record_a     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    record_b     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    record_type  text NOT NULL,
    -- Why they look alike: the namespace they share, such as email or phone.
    reason       text NOT NULL CHECK (reason ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$'),
    -- The shared values' namespaces and whether either side verified them.
    evidence     jsonb NOT NULL DEFAULT '{}',
    -- blocked: both have their own user id, so they can never be merged; they
    -- can be linked as the same human (a same_human relationship) instead.
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'blocked', 'merged', 'linked', 'dismissed')),
    decided_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    decided_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (record_a < record_b),
    UNIQUE (workspace_id, record_a, record_b)
);

CREATE INDEX record_merge_candidates_open_idx ON record_merge_candidates (workspace_id, status, created_at DESC, id DESC);
CREATE INDEX record_merge_candidates_b_idx ON record_merge_candidates (record_b);

CREATE TABLE record_merges (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The record kept, and the one merged into it (kept, marked merged_into).
    into_id      uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    from_id      uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- What moved: counts of identities, attributes, links, events and consents.
    moved        jsonb NOT NULL DEFAULT '{}',
    merged_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    request_id   text NOT NULL DEFAULT '',
    merged_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX record_merges_into_idx ON record_merges (into_id, merged_at DESC);

-- Records that already share an email or phone are asked about now.
INSERT INTO record_merge_candidates (workspace_id, record_a, record_b, record_type, reason, evidence, status)
SELECT DISTINCT ON (x.workspace_id, LEAST(x.record_id, y.record_id), GREATEST(x.record_id, y.record_id))
    x.workspace_id, LEAST(x.record_id, y.record_id), GREATEST(x.record_id, y.record_id), ra.record_type, x.namespace,
    jsonb_build_object('namespace', x.namespace, 'verified', x.verified OR y.verified),
    CASE WHEN EXISTS (SELECT 1 FROM record_identities u WHERE u.record_id = x.record_id AND u.namespace = 'user_id' AND u.valid_to IS NULL)
          AND EXISTS (SELECT 1 FROM record_identities u WHERE u.record_id = y.record_id AND u.namespace = 'user_id' AND u.valid_to IS NULL)
         THEN 'blocked' ELSE 'open' END
FROM record_identities x
JOIN record_identities y ON y.workspace_id = x.workspace_id AND y.namespace = x.namespace AND y.value = x.value
    AND y.record_id <> x.record_id AND y.valid_to IS NULL
JOIN records ra ON ra.id = x.record_id AND ra.merged_into IS NULL
JOIN records rb ON rb.id = y.record_id AND rb.merged_into IS NULL AND rb.record_type = ra.record_type
WHERE x.valid_to IS NULL AND x.namespace IN ('email', 'phone')
ON CONFLICT DO NOTHING;
