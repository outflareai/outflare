-- Who stands behind a brand, entered once under "About your company":
-- its legal name, its tax or company number (ABN, VAT, EIN) and its postal
-- address, in the same shape as a customer record's location attribute
-- (line1, line2, city, region, postal_code, country as an ISO 3166 code).
-- Lifecycle email footers, the brand's help centre, its cookie policy and
-- its agents read them from here, so nobody types the address into each
-- template.
ALTER TABLE brand_context ADD COLUMN legal_name text NOT NULL DEFAULT '';
ALTER TABLE brand_context ADD COLUMN tax_id text NOT NULL DEFAULT '';
ALTER TABLE brand_context ADD COLUMN address jsonb NOT NULL DEFAULT '{}';
