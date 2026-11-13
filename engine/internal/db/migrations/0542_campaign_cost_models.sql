-- A tracker campaign's cost model (plans/ad-tracker.md, B6): how the
-- tracker report works out what its clicks cost. token (the default, as
-- before) takes what the traffic source reported on each click with
-- {cost}; cpc a fixed amount a click; cpa a fixed amount a conversion;
-- revshare a share of each conversion's payout. Amounts are millionths of
-- cost_currency ('' for the traffic source's, else the brand's); the share
-- is in basis points.
ALTER TABLE tracking_campaigns
    ADD COLUMN cost_model         text    NOT NULL DEFAULT 'token' CHECK (cost_model IN ('token', 'cpc', 'cpa', 'revshare')),
    ADD COLUMN cost_amount_micros bigint  NOT NULL DEFAULT 0 CHECK (cost_amount_micros BETWEEN 0 AND 1000000000000),
    ADD COLUMN cost_share_bp      integer NOT NULL DEFAULT 0 CHECK (cost_share_bp BETWEEN 0 AND 10000),
    ADD COLUMN cost_currency      text    NOT NULL DEFAULT '' CHECK (cost_currency ~ '^([A-Z]{3})?$');
