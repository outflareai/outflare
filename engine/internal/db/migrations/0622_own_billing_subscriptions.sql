-- Subscriptions from a brand's own billing system, sent to the API
-- (POST /v1/brands/{brand}/billing-subscriptions) instead of read from a
-- payment connection. They are rows of payment_subscriptions with no
-- connection: provider is 'api' and provider_id is the sender's own id,
-- kept once per brand.
ALTER TABLE payment_subscriptions ALTER COLUMN integration_id DROP NOT NULL;

CREATE UNIQUE INDEX payment_subscriptions_own_idx ON payment_subscriptions (brand_id, provider_id) WHERE integration_id IS NULL;
