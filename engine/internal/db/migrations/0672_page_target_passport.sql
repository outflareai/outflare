-- A page's target carries what the page is for, besides its keywords: the
-- search intent it answers, the kind of page it is, and a short Markdown
-- summary of what the page holds (headings, plans and prices, features,
-- calls to action, questions answered), so its content can be checked
-- against the keywords and the intent. Empty means not set.
ALTER TABLE page_targets
    ADD COLUMN intent text NOT NULL DEFAULT ''
        CHECK (intent IN ('', 'informational', 'commercial', 'transactional', 'navigational')),
    ADD COLUMN page_type text NOT NULL DEFAULT ''
        CHECK (page_type IN ('', 'homepage', 'product', 'pricing', 'use-case', 'comparison', 'blog', 'location', 'tool', 'docs', 'other')),
    ADD COLUMN summary text NOT NULL DEFAULT ''
        CHECK (length(summary) <= 20000);
