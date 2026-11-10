-- Partner short-link clicks (/r/<code>) go into the one click table with
-- redirect link clicks (plans/ad-tracker.md, T2), so both are written the
-- same way (batched, with the visitor's region, city, network and device,
-- and a keyed address hash) and read in one place. A partner click names
-- its partner membership instead of a link. partner_clicks keeps the clicks
-- from before and imported ones, and stays readable.
--
-- Expand only: link_id only loses NOT NULL (the version before always sets
-- it, and only counts clicks by their link), and the rest are new columns
-- and a constraint every existing row meets.
ALTER TABLE tracking_link_clicks
    ALTER COLUMN link_id DROP NOT NULL,
    ADD COLUMN membership_id uuid REFERENCES partner_memberships (id) ON DELETE CASCADE,
    ADD COLUMN sub_id        text NOT NULL DEFAULT '' CHECK (length(sub_id) <= 100),
    ADD COLUMN referrer_host text NOT NULL DEFAULT '' CHECK (length(referrer_host) <= 255),
    ADD CONSTRAINT tracking_link_clicks_target CHECK (link_id IS NOT NULL OR membership_id IS NOT NULL);
CREATE INDEX tracking_link_clicks_membership_idx ON tracking_link_clicks (membership_id, clicked_at) WHERE membership_id IS NOT NULL;

-- A referral credited by a click in the merged table points at it here;
-- one credited by a click from before still points at partner_clicks.
ALTER TABLE partner_referrals
    ADD COLUMN link_click_id bigint REFERENCES tracking_link_clicks (id) ON DELETE SET NULL;
