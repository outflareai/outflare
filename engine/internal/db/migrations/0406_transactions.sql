-- Transactions (plans/customer-data-platform, the owner's decision of
-- 2026-11-02): one row per payment, refund, chargeback or credit, per
-- brand, from billing connectors and the API. From now on only: nothing
-- is rebuilt from past events.

-- The currency a brand reports money in; transactions are converted to it.
ALTER TABLE brands ADD COLUMN currency text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$');

CREATE TABLE transactions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id           uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The person or company who paid. A transaction is the business's
    -- record of money, so it stays when they are erased, unlinked.
    record_id          uuid REFERENCES records (id) ON DELETE SET NULL,
    kind               text NOT NULL CHECK (kind IN ('payment', 'refund', 'chargeback', 'credit')),
    status             text NOT NULL DEFAULT 'succeeded' CHECK (status IN ('succeeded', 'pending', 'failed', 'reversed')),
    -- In the currency's minor unit (cents), always positive: kind says
    -- which way the money went.
    amount_minor       bigint NOT NULL CHECK (amount_minor >= 0),
    currency           text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    -- The amount in the brand's currency at the day's rate; null until a
    -- rate for that day is known.
    brand_amount_minor bigint,
    brand_currency     text NOT NULL DEFAULT 'USD' CHECK (brand_currency ~ '^[A-Z]{3}$'),
    product_id         uuid REFERENCES products (id) ON DELETE SET NULL,
    -- Where it came from (api, or a billing connector such as stripe), and
    -- its id there: the same one twice is the same transaction.
    source             text NOT NULL CHECK (source ~ '^[a-z][a-z0-9_.-]{0,40}$'),
    source_id          text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 200),
    -- A refund or chargeback: the payment it gives back, by its source id.
    refund_of          text CHECK (length(refund_of) <= 200),
    description        text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    occurred_at        timestamptz NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, source, source_id)
);
CREATE INDEX transactions_brand_idx ON transactions (brand_id, occurred_at DESC, id DESC);
CREATE INDEX transactions_record_idx ON transactions (record_id, occurred_at DESC) WHERE record_id IS NOT NULL;
CREATE INDEX transactions_unconverted_idx ON transactions (occurred_at) WHERE brand_amount_minor IS NULL;

-- Query (docs/query.md).
CREATE VIEW query.transactions WITH (security_barrier) AS
SELECT t.id, t.brand_id, t.record_id, t.kind, t.status, t.amount_minor, t.currency, t.brand_amount_minor, t.brand_currency,
    t.product_id, t.source, t.source_id, t.refund_of, t.description, t.occurred_at, t.created_at
FROM transactions t JOIN query_scopes s ON s.pid = pg_backend_pid() AND t.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR t.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR t.brand_id = s.brand);
