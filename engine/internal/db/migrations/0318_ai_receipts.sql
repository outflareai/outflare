-- AI receipts (plans/ai-accounts, step 3): receipts people forward to the
-- workspace's inbound address, read and matched to an AI account, whose
-- price, seats and renewal they keep up to date. Only the details are
-- kept; the message itself is the inbound email's, deleted after 30 days.
CREATE TABLE ai_receipts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    inbound_email_id uuid REFERENCES inbound_emails (id) ON DELETE SET NULL,
    -- The AI company: anthropic, openai, cursor or github.
    vendor text NOT NULL CHECK (vendor IN ('anthropic', 'openai', 'cursor', 'github')),
    -- The AI tool it is for, when the receipt says (an ai_accounts provider).
    provider text,
    forwarded_by uuid REFERENCES users (id) ON DELETE SET NULL,
    charged_on date,
    -- What was charged, in the currency's minor unit (cents), and in US
    -- cents once known (usd_cents is null until a rate converts it).
    amount_minor bigint CHECK (amount_minor >= 0),
    currency text CHECK (currency ~ '^[A-Z]{3}$'),
    usd_cents bigint CHECK (usd_cents >= 0),
    plan text CHECK (length(plan) <= 100),
    seats integer CHECK (seats BETWEEN 1 AND 100000),
    period_start date,
    period_end date,
    -- The account email the receipt names, if any.
    account_email text CHECK (length(account_email) <= 254),
    -- Identifiers that tie later receipts to the same account: invoice
    -- number prefix, card ending.
    identifiers jsonb NOT NULL DEFAULT '{}',
    account_id uuid REFERENCES ai_accounts (id) ON DELETE SET NULL,
    -- How it was matched: account_email, known_id, forwarder,
    -- expected_renewal, or person (someone assigned it).
    matched_by text CHECK (matched_by IN ('account_email', 'known_id', 'forwarder', 'expected_renewal', 'person')),
    status text NOT NULL CHECK (status IN ('matched', 'needs_review', 'ignored')),
    resolved_by uuid REFERENCES users (id) ON DELETE SET NULL,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_receipts_workspace_idx ON ai_receipts (workspace_id, created_at DESC);
CREATE INDEX ai_receipts_review_idx ON ai_receipts (workspace_id) WHERE status = 'needs_review';
CREATE INDEX ai_receipts_account_idx ON ai_receipts (account_id) WHERE account_id IS NOT NULL;

-- Identifiers learned from matched receipts, so the next receipt with the
-- same one goes to the same account.
CREATE TABLE ai_receipt_ids (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('invoice_prefix', 'card_last4', 'customer')),
    value text NOT NULL CHECK (length(value) <= 200),
    account_id uuid NOT NULL REFERENCES ai_accounts (id) ON DELETE CASCADE,
    PRIMARY KEY (workspace_id, kind, value)
);
