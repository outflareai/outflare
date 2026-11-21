-- Shapes of a brand's own keys, hidden in recordings and heatmap pictures
-- on top of what is always hidden. A shape is written with x for any
-- letter or digit and the key's own separators, such as
-- xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx. Never a key itself.
ALTER TABLE replay_settings ADD COLUMN hide_shapes text[] NOT NULL DEFAULT '{}';
