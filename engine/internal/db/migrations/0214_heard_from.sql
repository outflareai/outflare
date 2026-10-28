-- "How did you hear about Outflare?": an optional question in Get started's
-- Star and follow step. One fixed choice per person, never free text.
ALTER TABLE users
    ADD COLUMN heard_from    text,
    ADD COLUMN heard_from_at timestamptz;
