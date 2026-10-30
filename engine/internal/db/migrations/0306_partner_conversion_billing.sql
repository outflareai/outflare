-- What a sale came through and how it bills, as Tolt's transactions say:
-- the payment provider (stripe, paddle, ...) and the billing interval.
-- Flows can test both.
ALTER TABLE partner_conversions
    ADD COLUMN payment_source text NOT NULL DEFAULT '' CHECK (payment_source = '' OR payment_source ~ '^[a-z0-9][a-z0-9_.-]{0,39}$'),
    ADD COLUMN billing_interval text NOT NULL DEFAULT '' CHECK (billing_interval IN ('', 'day', 'week', 'month', 'year', 'one_time'));
