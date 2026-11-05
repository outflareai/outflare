-- No-code A/B tests: the page a test was made for, such as
-- https://northpeak.example/pricing, so the test's page can open the visual
-- editor and previews there. Empty for tests of a flag made elsewhere.
ALTER TABLE ab_tests ADD COLUMN page text NOT NULL DEFAULT '';
