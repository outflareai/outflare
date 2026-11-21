-- What a person pinned to their menu in the web app, and the things they
-- opened last, kept with their account so both follow them to every
-- browser and device. ref names a page (its id, such as "sequences") or
-- one thing inside a page (a path of ids). It never holds a title or a
-- name: the web app works those out when it draws the menu. position
-- orders pins; at is when a recent was last opened.
CREATE TABLE user_menu_items (
    user_id  uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind     text NOT NULL CHECK (kind IN ('pin', 'recent')),
    ref      text NOT NULL,
    position integer NOT NULL DEFAULT 0,
    at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, kind, ref)
);
