-- How an offer's click id reaches whoever reports its conversions, so the
-- "missing click id" warning follows what a redirect really does:
--   auto          work it out: a {click_id} in the URL or the network's own
--                 parameter, else the ofclk for a page on the brand's domains
--   first_party   a page of the brand's own reads the ofclk every redirect adds
--   affiliate     someone else's page: the URL must pass {click_id} on
--   unattributed  no conversion is expected to name the click
ALTER TABLE tracker_offers
    ADD COLUMN click_id_delivery text NOT NULL DEFAULT 'auto'
        CHECK (click_id_delivery IN ('auto', 'first_party', 'affiliate', 'unattributed'));
