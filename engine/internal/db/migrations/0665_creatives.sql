-- Ad creatives a brand makes from saved recipes.
--
-- A skill is a recipe for one kind of ad: its prompts, the AI model and
-- the sizes. Every saved change to the recipe is a new version, so a
-- creative always says which version and which prompt made it, and a
-- version is never edited. A version holds one prompt, or several side
-- by side to see which makes better ads.
--
-- A creative is one ad made from a skill for one persona and one pain:
-- the request to the brand's own AI account (brand_generations), the
-- words laid over the picture, its tags, and a person's yes or no.

CREATE TABLE creative_skills (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description     text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    kind            text NOT NULL CHECK (kind IN ('image', 'video')),
    enabled         boolean NOT NULL DEFAULT true,
    current_version integer NOT NULL DEFAULT 1,
    archived_at     timestamptz,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX creative_skills_name_idx ON creative_skills (brand_id, lower(name)) WHERE archived_at IS NULL;
CREATE INDEX creative_skills_brand_idx ON creative_skills (workspace_id, brand_id);

CREATE TABLE creative_skill_versions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    skill_id     uuid NOT NULL REFERENCES creative_skills (id) ON DELETE CASCADE,
    version      integer NOT NULL,
    -- What changed, in the words of whoever saved it.
    note         text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    -- The connector that makes it ('' for the first one connected), and
    -- the provider's own id for the model.
    provider     text NOT NULL DEFAULT '',
    model        text NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
    options      jsonb NOT NULL DEFAULT '{}',
    sizes        text[] NOT NULL DEFAULT '{}',
    -- Rules added to every prompt of this version.
    rules        text NOT NULL DEFAULT '' CHECK (length(rules) <= 4000),
    -- The prompts: [{key, name, picture, copy, share}].
    variants     jsonb NOT NULL,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (skill_id, version)
);
CREATE INDEX creative_skill_versions_brand_idx ON creative_skill_versions (workspace_id, brand_id);

CREATE TABLE creatives (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- Counts up within a brand: the creative's short name (CR-0142).
    number        integer NOT NULL,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    kind          text NOT NULL CHECK (kind IN ('image', 'video')),
    skill_id      uuid REFERENCES creative_skills (id) ON DELETE SET NULL,
    skill_version integer NOT NULL DEFAULT 0,
    variant       text NOT NULL DEFAULT '',
    -- Who it is for, what it is about and how it opens: what results
    -- are compared by.
    persona       text NOT NULL DEFAULT '' CHECK (length(persona) <= 200),
    pain          text NOT NULL DEFAULT '' CHECK (length(pain) <= 200),
    hook          text NOT NULL DEFAULT '' CHECK (length(hook) <= 200),
    tags          text[] NOT NULL DEFAULT '{}',
    -- The words laid over the picture.
    headline      text NOT NULL DEFAULT '' CHECK (length(headline) <= 200),
    subline       text NOT NULL DEFAULT '' CHECK (length(subline) <= 300),
    cta           text NOT NULL DEFAULT '' CHECK (length(cta) <= 60),
    -- The prompt as it was sent, with every fill-in filled.
    prompt        text NOT NULL DEFAULT '',
    generation_id uuid REFERENCES brand_generations (id) ON DELETE SET NULL,
    -- A person's answer: nothing reaches an ad network without a yes.
    review        text NOT NULL DEFAULT 'waiting' CHECK (review IN ('waiting', 'approved', 'rejected')),
    review_reason text NOT NULL DEFAULT '' CHECK (length(review_reason) <= 500),
    reviewed_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    reviewed_at   timestamptz,
    version       integer NOT NULL DEFAULT 1,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, number)
);
CREATE INDEX creatives_brand_idx ON creatives (workspace_id, brand_id, created_at DESC);
CREATE INDEX creatives_skill_idx ON creatives (skill_id, skill_version);
CREATE INDEX creatives_generation_idx ON creatives (generation_id);
