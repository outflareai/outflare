-- The Paid ads Overview's own numbers. Beside what a new customer and a
-- sign-up may cost, a brand can say how many new customers it wants in a
-- month, what it may spend in a month, the revenue it wants from ads in a
-- month and the return on ad spend it aims for, so every number on the
-- Overview is judged against a target. overview_numbers is which numbers
-- the Overview shows and in what order; empty shows the usual six.
ALTER TABLE ad_brand_economics
    ADD COLUMN target_new_payers_monthly     integer CHECK (target_new_payers_monthly > 0),
    ADD COLUMN monthly_budget_micros         bigint CHECK (monthly_budget_micros > 0),
    ADD COLUMN target_revenue_monthly_micros bigint CHECK (target_revenue_monthly_micros > 0),
    ADD COLUMN target_roas                   numeric(8, 2) CHECK (target_roas > 0),
    ADD COLUMN overview_numbers              text[] NOT NULL DEFAULT '{}';
