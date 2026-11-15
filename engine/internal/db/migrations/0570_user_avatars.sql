-- A person's profile photo: the picture on the Google account they sign in
-- with, saved at each Google sign-in. Shown beside their name wherever
-- someone is assigned something (who manages a site on Presence, a
-- record's owner). Empty means no photo: the app shows their initials.
ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url text NOT NULL DEFAULT '';
