-- Send-back reads the workspace's conversion definitions: a rule can name
-- one instead of a bare event, and then sends only the events that meet its
-- conditions, worth what it says, as each network calls it. matched_until
-- is how far through the event stream a rule whose definition has
-- conditions has checked them, so each event is looked at once.
ALTER TABLE ad_conversion_rules ADD COLUMN conversion_id uuid REFERENCES conversion_definitions (id) ON DELETE SET NULL;
ALTER TABLE ad_conversion_rules ADD COLUMN matched_until timestamptz;
