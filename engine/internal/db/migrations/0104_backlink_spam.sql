-- Link spam: a score from 0 to 100 on each backlink, with the factors
-- behind it (package seosync), and a person's review of it. Nothing is
-- disavowed on its own: the disavow file lists only the links and domains
-- people marked.

ALTER TABLE backlinks
    ADD COLUMN spam_score    smallint CHECK (spam_score BETWEEN 0 AND 100),
    ADD COLUMN spam_factors  jsonb NOT NULL DEFAULT '[]',
    -- '' (not reviewed), ok (a real link), disavow (ask Google to ignore it).
    ADD COLUMN review        text NOT NULL DEFAULT '' CHECK (review IN ('', 'ok', 'disavow')),
    ADD COLUMN review_note   text NOT NULL DEFAULT '',
    ADD COLUMN reviewed_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    ADD COLUMN reviewed_at   timestamptz;

CREATE INDEX backlinks_spam_idx ON backlinks (workspace_id, site_id, spam_score DESC) WHERE status = 'live';
