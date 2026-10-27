-- Community links: the optional "Star us on GitHub" and "Follow us on X"
-- items on Home and Get started. Nobody can check a star or a follow, so
-- opening the link ticks the item; each person keeps their own.
ALTER TABLE users
    -- Links the person opened, such as {github,x}.
    ADD COLUMN community_clicked   text[] NOT NULL DEFAULT '{}',
    -- Set when the person hid the items; they stay hidden.
    ADD COLUMN community_hidden_at timestamptz;
