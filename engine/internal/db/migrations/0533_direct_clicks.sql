-- Direct mode (plans/ad-tracker.md, T8): an ad that links straight to the
-- brand's page, with the campaign's ofc code, gets a tracker click from the
-- visit itself: the site script names a new click id and the visit records
-- the click, with the traffic source's parameters from the page's URL. Such
-- a click had no redirect. Only adds; the default is constant, so adding it
-- doesn't rewrite the table.
ALTER TABLE tracking_link_clicks ADD COLUMN direct boolean NOT NULL DEFAULT false;
