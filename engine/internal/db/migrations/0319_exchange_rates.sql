-- Exchange rates (plans/ai-accounts, step 3): the European Central Bank's
-- daily reference rates, as US dollars per unit of each currency, so a
-- receipt charged in another currency gets its amount in US dollars. The
-- same for every workspace: public data, read without a key.
CREATE TABLE exchange_rates (
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    day date NOT NULL,
    usd_per_unit numeric NOT NULL CHECK (usd_per_unit > 0),
    PRIMARY KEY (currency, day)
);
