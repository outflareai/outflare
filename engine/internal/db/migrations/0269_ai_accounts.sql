-- AI Accounts (plans/ai-accounts): the AI subscriptions and API accounts
-- the workspace's people pay for. People add them by hand for now;
-- receipts, Outflare for desktop and admin keys fill them in later.
CREATE TABLE ai_accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The AI tool: claude, chatgpt, cursor, copilot, anthropic_api,
    -- openai_api. The app shows its logo.
    provider text NOT NULL CHECK (provider IN ('claude', 'chatgpt', 'cursor', 'copilot', 'anthropic_api', 'openai_api')),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- The email the account is on at the AI company (its login).
    account_email text CHECK (length(account_email) <= 254),
    plan text CHECK (length(plan) <= 100),
    seats integer CHECK (seats BETWEEN 1 AND 100000),
    -- Who pays: the company, or the person themselves. Only company-paid
    -- accounts count in spend.
    paid_by text NOT NULL DEFAULT 'company' CHECK (paid_by IN ('company', 'person')),
    billing_cycle text CHECK (billing_cycle IN ('monthly', 'yearly', 'prepaid', 'usage')),
    renews_on date,
    -- The price per billing cycle, in US cents.
    price_cents bigint CHECK (price_cents BETWEEN 0 AND 100000000000),
    -- Banked resets the AI company lets the account use (Codex resets,
    -- Claude reset grants); null when unknown.
    resets_available integer CHECK (resets_available BETWEEN 0 AND 1000),
    cancelled_at timestamptz,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_accounts_workspace_idx ON ai_accounts (workspace_id, provider);

-- The people an AI account belongs to: its owner, and anyone else who
-- signs in to it. A person who leaves the workspace stays here, so the
-- account shows "Owner left" until someone takes it over.
CREATE TABLE ai_account_members (
    account_id uuid NOT NULL REFERENCES ai_accounts (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role text NOT NULL DEFAULT 'owner' CHECK (role IN ('owner', 'user')),
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, user_id)
);
CREATE INDEX ai_account_members_user_idx ON ai_account_members (workspace_id, user_id);
