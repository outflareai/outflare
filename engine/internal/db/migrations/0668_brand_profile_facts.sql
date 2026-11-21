-- The brand profile: who the company is, what it sells, who it serves and
-- why it is different, kept as separate facts an agent can cite rather
-- than a few long text boxes. Each fact sits in one section, says where it
-- came from and how sure it is, whether it may be said to customers or is
-- for the team only, and whether a person has confirmed it. A fact an
-- agent found stays "suggested" until a person confirms it, and a
-- suggested fact is never used with customers.
CREATE TABLE brand_profile_facts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    section       text NOT NULL CHECK (section IN ('company', 'offer', 'customers', 'positioning', 'proof',
                      'competitors', 'objections', 'voice', 'goals', 'channels', 'rules')),
    -- A short name for the fact, such as "What we sell".
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 200),
    text          text NOT NULL CHECK (length(text) BETWEEN 1 AND 10000),
    -- Where it came from: a person typed it, the onboarding interview, the
    -- brand's website, conversations with customers, an agent's research,
    -- an import, or the brand's older context fields.
    source        text NOT NULL DEFAULT 'person' CHECK (source IN ('person', 'interview', 'website',
                      'conversation', 'agent', 'import', 'context')),
    -- Why it is believed: a list of {note, ref}.
    evidence      jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence) = 'array'),
    confidence    text NOT NULL DEFAULT 'medium' CHECK (confidence IN ('low', 'medium', 'high')),
    -- customers: may be said to customers. internal: for the team and
    -- its agents only.
    visibility    text NOT NULL DEFAULT 'internal' CHECK (visibility IN ('customers', 'internal')),
    status        text NOT NULL DEFAULT 'confirmed' CHECK (status IN ('suggested', 'confirmed', 'retired')),
    -- A suggested fact that would replace this one once confirmed.
    replaces_id   uuid REFERENCES brand_profile_facts (id) ON DELETE SET NULL,
    -- Set while the fact mirrors one of the brand's older context fields
    -- (about, audience, goals, voice, rules): the two change together.
    context_field text CHECK (context_field IN ('about', 'audience', 'goals', 'voice', 'rules')),
    -- When to look at it again; null for a fact with no review date.
    review_at     timestamptz,
    confirmed_by  uuid REFERENCES actors (id) ON DELETE SET NULL,
    confirmed_at  timestamptz,
    retired_at    timestamptz,
    position      integer NOT NULL DEFAULT 0,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX brand_profile_facts_brand_idx ON brand_profile_facts (brand_id, section, status);
CREATE INDEX brand_profile_facts_workspace_idx ON brand_profile_facts (workspace_id, created_at DESC);
CREATE UNIQUE INDEX brand_profile_facts_context_idx ON brand_profile_facts (brand_id, context_field) WHERE context_field IS NOT NULL;
