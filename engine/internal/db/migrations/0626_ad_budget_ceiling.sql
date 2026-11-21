-- A brand's ceiling on its total daily ad budget: the most the daily
-- budgets of its active campaigns may add up to, in micros of the brand's
-- ad currency. A raise that would pass it is refused unless a signed-in
-- person repeats it knowingly. Null: no ceiling.
ALTER TABLE ad_brand_economics
    ADD COLUMN IF NOT EXISTS daily_budget_ceiling_micros bigint CHECK (daily_budget_ceiling_micros > 0);
