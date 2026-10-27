-- Search: full-text search over customer records and their history (emails
-- sent and received, meetings, deals, lifecycle email), in Postgres itself.
-- Each kind has an immutable function that builds its search vector and an
-- index on it; search calls the same function, so the index always applies
-- and nothing has to be kept in sync. The 'simple' configuration doesn't
-- stem words, so names, emails and words in any language match as written;
-- search matches prefixes, so "pric" finds "pricing".

CREATE FUNCTION search_vector_record(name text, attributes jsonb) RETURNS tsvector
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT setweight(to_tsvector('simple', coalesce(name, '')), 'A') || setweight(jsonb_to_tsvector('simple', coalesce(attributes, '{}'), '["string"]'), 'C')
$$;

CREATE FUNCTION search_vector_identity(value text) RETURNS tsvector
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT to_tsvector('simple', coalesce(value, '') || ' ' || replace(replace(coalesce(value, ''), '@', ' '), '.', ' '))
$$;

CREATE FUNCTION search_vector_message(subject text, body text) RETURNS tsvector
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT setweight(to_tsvector('simple', coalesce(subject, '')), 'A') || setweight(to_tsvector('simple', left(coalesce(body, ''), 20000)), 'D')
$$;

CREATE FUNCTION search_vector_title(title text, detail text) RETURNS tsvector
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT setweight(to_tsvector('simple', coalesce(title, '')), 'A') || setweight(to_tsvector('simple', left(coalesce(detail, ''), 20000)), 'C')
$$;

CREATE INDEX records_search_idx ON records USING gin (search_vector_record(name, attributes));
CREATE INDEX record_identities_search_idx ON record_identities USING gin (search_vector_identity(value)) WHERE valid_to IS NULL;
-- Outreach emails are written from templates, so their subjects are
-- enough, and the index stays small; replies are searched in full.
CREATE INDEX messages_search_idx ON messages USING gin (search_vector_message(subject, NULL));
CREATE INDEX inbound_messages_search_idx ON inbound_messages USING gin (search_vector_message(subject, body));
CREATE INDEX meetings_search_idx ON meetings USING gin (search_vector_title(title, notes));
CREATE INDEX deals_search_idx ON deals USING gin (search_vector_title(name, stage));
CREATE INDEX emails_search_idx ON emails USING gin (search_vector_title(subject, NULL));
