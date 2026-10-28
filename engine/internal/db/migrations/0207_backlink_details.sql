-- Backlink details, as Ahrefs shows them: the linking page's strength,
-- traffic and keywords, what kind of page it is, what kind of link it is,
-- and where the linked URL leads (its redirects). Read with the links
-- (package seosync), so filters and grouping read only what is stored.

ALTER TABLE backlinks
    -- The linking page's strength, 0 to 100 (Ahrefs UR).
    ADD COLUMN url_rating             double precision,
    -- Estimated monthly organic visits to the linking domain and page, read
    -- only when the connection asks for traffic.
    ADD COLUMN domain_traffic         double precision,
    ADD COLUMN page_traffic           double precision,
    -- Keywords the linking page ranks for in the top 100.
    ADD COLUMN page_keywords          double precision,
    -- Domains linking to the linking page, and domains it links to.
    ADD COLUMN page_referring_domains double precision,
    ADD COLUMN linked_domains         double precision,
    ADD COLUMN external_links         double precision,
    -- text, image, redirect, canonical, alternate, frame, form or rss.
    ADD COLUMN link_type              text NOT NULL DEFAULT '',
    -- The link sits in the page's main content.
    ADD COLUMN in_content             boolean NOT NULL DEFAULT false,
    -- The linking page's kind and topics as paths (/Article/Listicle).
    ADD COLUMN page_types             text[] NOT NULL DEFAULT '{}',
    ADD COLUMN page_categories        text[] NOT NULL DEFAULT '{}',
    ADD COLUMN languages              text[] NOT NULL DEFAULT '{}',
    ADD COLUMN platforms              text[] NOT NULL DEFAULT '{}',
    ADD COLUMN title                  text NOT NULL DEFAULT '',
    ADD COLUMN author                 text NOT NULL DEFAULT '',
    -- Where the linked URL leads: [{kind: "301" | "canonical", url}].
    ADD COLUMN hops                   jsonb NOT NULL DEFAULT '[]',
    -- When a provider last found the link live.
    ADD COLUMN last_seen_at           timestamptz;

CREATE INDEX backlinks_first_seen_idx ON backlinks (workspace_id, site_id, first_seen_at DESC);
CREATE INDEX backlinks_source_url_idx ON backlinks (workspace_id, site_id, source_url);
