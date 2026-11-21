-- Link orders: the live-link check compares the anchor on the donor page
-- with the ordered primary and second anchors. exact: the link text is one
-- of them (ignoring case, spacing and punctuation); partial: one contains
-- the other or they share most words; different: neither; '' when no
-- anchor was ordered or no link was seen. It is a warning only: a
-- different anchor never marks an order lost.
--
-- Has a default, so the previous release keeps writing rows without it.
ALTER TABLE coverage_link_orders
    ADD COLUMN last_check_anchor_match text NOT NULL DEFAULT ''
        CHECK (last_check_anchor_match IN ('', 'exact', 'partial', 'different'));
