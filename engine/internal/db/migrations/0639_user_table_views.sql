-- The columns a person chose to see in a named table of the web app
-- ("Save as my default"), kept with their account so the view follows them
-- to every browser and device. hidden is the ids of the columns they hid.
CREATE TABLE user_table_views (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    view       text NOT NULL,
    hidden     text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, view)
);
