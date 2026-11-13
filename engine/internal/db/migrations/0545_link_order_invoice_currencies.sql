-- Link orders: an invoice in any common currency (GBP, PLN, CHF and so on),
-- kept as it is on the invoice and converted to US dollars at the
-- ECB reference rate on the invoice date. Accounting only: nothing here sends
-- money.
--
-- quoted_invoice_* is the quote as invoiced; invoice_* is the same, frozen
-- at Approve and refreshed at Mark Paid when the final invoice has its own
-- date or rate. *_usd_minor is the US-dollar equivalent in cents; fx_rate is
-- US dollars per one unit of the invoice currency; fx_rate_date is the day
-- the rate is for; fx_rate_source is 'usd' (no conversion) or 'ecb' (the
-- European Central Bank reference rate Outflare stores, for the invoice
-- date or the last working day within ten days before it). Nobody types a
-- rate in.
--
-- The existing quoted_currency / currency columns keep their USD-or-EUR
-- check, so the previous release reads every row as before: for an invoice
-- in USD or EUR they hold that amount; for any other currency they hold
-- the US-dollar equivalent in USD, which budgets and totals use.
--
-- Every column is nullable, so the previous release keeps writing rows
-- without them. Only adds, so the version before still works.
ALTER TABLE coverage_link_orders
    ADD COLUMN quoted_invoice_currency text CHECK (quoted_invoice_currency IS NULL OR quoted_invoice_currency ~ '^[A-Z]{3}$'),
    ADD COLUMN quoted_invoice_amount_minor bigint CHECK (quoted_invoice_amount_minor IS NULL OR quoted_invoice_amount_minor >= 0),
    ADD COLUMN quoted_invoice_date date,
    ADD COLUMN quoted_usd_minor bigint CHECK (quoted_usd_minor IS NULL OR quoted_usd_minor >= 0),
    ADD COLUMN quoted_fx_rate numeric(24, 12) CHECK (quoted_fx_rate IS NULL OR quoted_fx_rate > 0),
    ADD COLUMN quoted_fx_rate_date date,
    ADD COLUMN quoted_fx_rate_source text CHECK (quoted_fx_rate_source IS NULL OR quoted_fx_rate_source IN ('usd', 'ecb')),
    ADD COLUMN invoice_currency text CHECK (invoice_currency IS NULL OR invoice_currency ~ '^[A-Z]{3}$'),
    ADD COLUMN invoice_amount_minor bigint CHECK (invoice_amount_minor IS NULL OR invoice_amount_minor >= 0),
    ADD COLUMN invoice_date date,
    ADD COLUMN usd_amount_minor bigint CHECK (usd_amount_minor IS NULL OR usd_amount_minor >= 0),
    ADD COLUMN fx_rate numeric(24, 12) CHECK (fx_rate IS NULL OR fx_rate > 0),
    ADD COLUMN fx_rate_date date,
    ADD COLUMN fx_rate_source text CHECK (fx_rate_source IS NULL OR fx_rate_source IN ('usd', 'ecb'));

-- The payment row Mark Paid records keeps the invoice currency and amount
-- in currency / amount_minor, and the US-dollar equivalent beside it.
ALTER TABLE coverage_payments
    ADD COLUMN usd_amount_minor bigint CHECK (usd_amount_minor IS NULL OR usd_amount_minor >= 0),
    ADD COLUMN fx_rate numeric(24, 12) CHECK (fx_rate IS NULL OR fx_rate > 0),
    ADD COLUMN fx_rate_date date,
    ADD COLUMN fx_rate_source text CHECK (fx_rate_source IS NULL OR fx_rate_source IN ('usd', 'ecb'));
