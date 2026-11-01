-- A program's ad and social click blocks: a website visit with a partner's
-- ref that came from a paid ad click (it carries an ad network's click id)
-- or from social media (fbclid, or a social network as referrer) doesn't
-- credit the partner.
ALTER TABLE partner_programs
    ADD COLUMN block_ad_clicks     boolean NOT NULL DEFAULT false,
    ADD COLUMN block_social_clicks boolean NOT NULL DEFAULT false;
