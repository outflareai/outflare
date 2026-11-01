-- Ad sets (Meta) and ad groups (Google) sync as level ad_group, and their
-- ads as level ad, under the campaign rows ad_entities already has. Details
-- is what the platform says about an ad: its headline, text, landing page
-- and preview image. Only campaign-days go to the spend ledger, so the
-- levels below never count spend twice.

ALTER TABLE ad_entities ADD COLUMN details jsonb NOT NULL DEFAULT '{}';
CREATE INDEX ad_entities_parent_idx ON ad_entities (parent_id) WHERE parent_id IS NOT NULL;
