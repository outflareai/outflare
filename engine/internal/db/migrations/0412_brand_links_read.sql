-- The last time Outflare read a brand's website for its links, and why
-- that read failed ('' when it worked): blocked (a bot check), slow_page,
-- or a connector code. The Links panel says why no suggestions came.
ALTER TABLE brands ADD COLUMN links_read_at timestamptz;
ALTER TABLE brands ADD COLUMN links_read_error text NOT NULL DEFAULT '';
