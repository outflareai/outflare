-- Which AI model a workspace uses to plan missions and run Outflare's own
-- agent, what it costs, and the most the workspace lets it spend a month.
-- The key itself stays sealed on the integration; this row only points at it.
CREATE TABLE workspace_ai_settings (
    workspace_id        uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    -- A connected AI model integration (claude, openai, gemini, mistral) in
    -- the same workspace. Null: the first connected one, in that order.
    integration_id      uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The provider's model id. Empty: the provider's default, where it has one.
    model               text NOT NULL DEFAULT '' CHECK (length(model) <= 200),
    -- US cents per million tokens, for providers whose prices Outflare does
    -- not know. Spend limits need them.
    input_cents_per_m   numeric(12, 4) CHECK (input_cents_per_m > 0),
    output_cents_per_m  numeric(12, 4) CHECK (output_cents_per_m > 0),
    -- The most AI calls may cost in a calendar month (UTC), across every
    -- mission and plan. Null: no workspace limit.
    monthly_limit_cents bigint DEFAULT 5000 CHECK (monthly_limit_cents >= 0),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    updated_by          uuid REFERENCES actors (id) ON DELETE SET NULL
);
