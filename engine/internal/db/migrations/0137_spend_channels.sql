-- Which marketing channel and brand each charge belongs to, so cost per
-- channel and per brand add up in one place: paid ads, outbound email,
-- SEO, affiliates and the rest. '' means not assigned yet; a job fills it
-- from the provider. Ad spend also names the campaign it paid for.
ALTER TABLE spend_entries
    ADD COLUMN channel text NOT NULL DEFAULT '' CHECK (channel IN ('', 'outbound_email', 'seo', 'ai_answers',
        'organic_social', 'reddit', 'paid_ads', 'affiliates', 'lifecycle_email', 'referral', 'direct', 'shared')),
    ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL,
    ADD COLUMN ad_entity_id uuid;

ALTER TABLE spend_recurring
    ADD COLUMN channel text NOT NULL DEFAULT '' CHECK (channel IN ('', 'outbound_email', 'seo', 'ai_answers',
        'organic_social', 'reddit', 'paid_ads', 'affiliates', 'lifecycle_email', 'referral', 'direct', 'shared')),
    ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;

CREATE INDEX spend_entries_channel_idx ON spend_entries (workspace_id, channel, occurred_at);
