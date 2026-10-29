-- Digital PR stories: the brand's experts, the stories it pitches, the
-- facts behind every number and the quotes its experts approved. A story
-- is checked before it can be approved: every number has a source, every
-- quote is one an expert approved, and a person approves the release.

-- A person a brand quotes: a person record (the name lives there), their
-- title and the topics they speak to. One expert per brand is the default.
CREATE TABLE pr_experts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- How a journalist should introduce them, such as "CEO".
    title        text NOT NULL DEFAULT '' CHECK (length(title) <= 200),
    topics       text[] NOT NULL DEFAULT '{}' CHECK (cardinality(topics) <= 30),
    is_default   boolean NOT NULL DEFAULT false,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pr_experts_brand_idx ON pr_experts (workspace_id, brand_id);
CREATE INDEX pr_experts_record_idx ON pr_experts (workspace_id, record_id);
CREATE UNIQUE INDEX pr_experts_default_idx ON pr_experts (brand_id) WHERE is_default;

-- A story: an idea, then a press release, checked and approved by a
-- person before it can be pitched.
CREATE TABLE stories (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- data: a finding from numbers; reactive: comment on today's news;
    -- comment: an expert's view on a topic.
    kind          text NOT NULL DEFAULT 'data' CHECK (kind IN ('data', 'reactive', 'comment')),
    title         text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    summary       text NOT NULL DEFAULT '' CHECK (length(summary) <= 2000),
    -- The press release, as Markdown.
    release       text NOT NULL DEFAULT '' CHECK (length(release) <= 20000),
    -- The news article a reactive story answers.
    article_id    uuid REFERENCES news_articles (id) ON DELETE SET NULL,
    -- idea, draft (being written), approved (checked and approved by a
    -- person; any change moves it back to draft), archived.
    status        text NOT NULL DEFAULT 'idea' CHECK (status IN ('idea', 'draft', 'approved', 'archived')),
    -- Not to be published before this time.
    embargo_until timestamptz,
    -- The latest check: {"ok", "errors": [...], "warnings": [...]}.
    last_check    jsonb,
    checked_at    timestamptz,
    approved_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at   timestamptz,
    -- manual, api, or the feature that suggested it.
    source        text NOT NULL DEFAULT 'manual' CHECK (length(source) <= 100),
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX stories_ws_idx ON stories (workspace_id, status, updated_at DESC);

-- A fact a story states, and where it comes from. Every number in the
-- release must appear in one of these.
CREATE TABLE story_facts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    story_id     uuid NOT NULL REFERENCES stories (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    statement    text NOT NULL CHECK (length(statement) BETWEEN 1 AND 1000),
    -- own_data (the brand's own numbers, aggregate only), public_data,
    -- news, brand_fact, or other.
    source_kind  text NOT NULL CHECK (source_kind IN ('own_data', 'public_data', 'news', 'brand_fact', 'other')),
    source_url   text NOT NULL DEFAULT '' CHECK (length(source_url) <= 2048),
    -- How to find it again: a saved query's name, a report, a page.
    source_note  text NOT NULL DEFAULT '' CHECK (length(source_note) <= 1000),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX story_facts_story_idx ON story_facts (story_id);

-- A quote by one of the brand's experts. Only approved quotes may appear
-- in a release; changing the words takes the approval away.
CREATE TABLE story_quotes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    story_id      uuid NOT NULL REFERENCES stories (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    expert_id     uuid NOT NULL REFERENCES pr_experts (id) ON DELETE CASCADE,
    body          text NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    status        text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'approved', 'withdrawn')),
    -- How the expert said yes, such as "by email on 29 Oct".
    approval_note text NOT NULL DEFAULT '' CHECK (length(approval_note) <= 500),
    approved_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at   timestamptz,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX story_quotes_story_idx ON story_quotes (story_id);
