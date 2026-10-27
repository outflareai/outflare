-- Website tracking keeps a page's whole query string, so reports can filter
-- on any parameter. A site can leave more out by name; sign-in tokens,
-- passwords, codes and email addresses are always dropped in code.
ALTER TABLE site_keys ADD COLUMN excluded_params text[] NOT NULL DEFAULT '{}';
