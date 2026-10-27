-- Subscription topics for lifecycle email: what people can choose to get,
-- such as product updates or a newsletter. Who chose what is a consent on
-- their customer record (channel email, topic = key), not stored here.
CREATE TABLE email_topics (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The name consents and the API use. It never changes, so consents
    -- keep pointing at the topic after a rename.
    key                   text NOT NULL,
    name                  text NOT NULL,
    description           text NOT NULL DEFAULT '',
    -- true: everyone gets it until they opt out. false: only people who
    -- opted in.
    subscribed_by_default boolean NOT NULL DEFAULT true,
    -- public topics are listed on the preference page; private ones only
    -- show there to people who are subscribed.
    visibility            text NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'private')),
    status                text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, key)
);

-- A lifecycle template, or one send, can belong to a topic.
ALTER TABLE email_templates ADD COLUMN topic_id uuid REFERENCES email_topics (id) ON DELETE SET NULL;
ALTER TABLE emails ADD COLUMN topic_id uuid REFERENCES email_topics (id) ON DELETE SET NULL;
CREATE INDEX emails_topic ON emails (workspace_id, topic_id, created_at DESC) WHERE topic_id IS NOT NULL;
