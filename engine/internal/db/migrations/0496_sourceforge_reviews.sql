-- SourceForge listings (business software products and open source
-- projects) are review pages too, found and read like G2 and Capterra.
-- Only widens the check, so the version before still works.
ALTER TABLE review_pages DROP CONSTRAINT review_pages_platform_check;
ALTER TABLE review_pages ADD CONSTRAINT review_pages_platform_check
    CHECK (platform IN ('trustpilot', 'g2', 'capterra', 'sourceforge', 'google_reviews'));
