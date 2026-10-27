-- Brand access: keep a person or an API token to some of a workspace's
-- brands. A brand-limited caller sees its brands' records and the shared
-- ones (no brand), changes only its brands' records, and is let in only on
-- the endpoints that know about brands. Owners and admins always see every
-- brand.

-- The brands an operator or viewer works on. Empty means every brand.
ALTER TABLE memberships ADD COLUMN brand_ids uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE invites ADD COLUMN brand_ids uuid[] NOT NULL DEFAULT '{}';

-- A token bound to one brand. Deleting the brand deletes its tokens, so a
-- token never widens to the whole workspace. A token is bound to a brand
-- or to a project (which has its own brand), never both.
ALTER TABLE api_tokens ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE CASCADE;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_brand_or_project CHECK (brand_id IS NULL OR project_id IS NULL);
CREATE INDEX api_tokens_brand_idx ON api_tokens (brand_id) WHERE brand_id IS NOT NULL;
