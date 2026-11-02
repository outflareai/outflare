-- The knowledge review queue: answers Outflare found and conflicts it
-- noticed, waiting for a person. Nothing here reaches customers or any AI
-- until someone approves it: an approved answer becomes a knowledge item,
-- an approved conflict retires the items that were wrong.

CREATE TABLE knowledge_suggestions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE CASCADE,
    -- answer: a proposed answer to a question the knowledge didn't cover.
    -- conflict: items that disagree.
    kind          text NOT NULL CHECK (kind IN ('answer', 'conflict')),
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    -- The question it answers, or what the items disagree about.
    question      text NOT NULL DEFAULT '' CHECK (length(question) <= 1000),
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 300),
    body          text NOT NULL DEFAULT '' CHECK (length(body) <= 20000),
    visibility    text NOT NULL DEFAULT 'customers_ai' CHECK (visibility IN ('public', 'customers_ai', 'team')),
    -- Where it came from: a teammate's reply in support, the business's
    -- website, Slack, an AI agent, a person, or an answer that noticed a
    -- conflict. ref is the conversation id or the page's address; quote is
    -- the words it was taken from.
    source        text NOT NULL CHECK (source IN ('support_reply', 'website', 'slack', 'agent', 'person', 'answer')),
    source_ref    text NOT NULL DEFAULT '' CHECK (length(source_ref) <= 1000),
    source_quote  text NOT NULL DEFAULT '' CHECK (length(source_quote) <= 4000),
    -- conflict: the items that disagree.
    item_ids      uuid[] NOT NULL DEFAULT '{}',
    -- Prices, refunds, legal or security: a person should read it twice.
    sensitive     boolean NOT NULL DEFAULT false,
    -- The same suggestion is waiting at most once.
    dedupe_key    text NOT NULL CHECK (length(dedupe_key) <= 1200),
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    decided_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    decided_at    timestamptz,
    reason        text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000),
    -- The item an approved answer became.
    item_id       uuid REFERENCES knowledge_items (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX knowledge_suggestions_queue ON knowledge_suggestions (workspace_id, status, created_at DESC);
CREATE UNIQUE INDEX knowledge_suggestions_waiting ON knowledge_suggestions (workspace_id, dedupe_key) WHERE status = 'pending';
-- A rejected suggestion isn't made again.
CREATE INDEX knowledge_suggestions_rejected ON knowledge_suggestions (workspace_id, dedupe_key) WHERE status = 'rejected';

-- A support conversation whose suggested reply found no answer: the
-- question, so the team's own reply can be offered as the answer.
ALTER TABLE support_conversations ADD COLUMN knowledge_gap text CHECK (length(knowledge_gap) <= 1000);
