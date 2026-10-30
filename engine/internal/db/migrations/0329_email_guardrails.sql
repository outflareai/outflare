-- Guardrails on marketing email, per workspace (emails.Guardrails). On by
-- default: each person gets at most 1 marketing email a day and 3 a week,
-- and none between 8pm and 8am in their time zone (held until morning).
-- Transactional email is never capped or held. A limit of 0 turns that
-- limit off.
ALTER TABLE workspaces
    ADD COLUMN email_cap_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN email_cap_per_day integer NOT NULL DEFAULT 1 CHECK (email_cap_per_day BETWEEN 0 AND 100),
    ADD COLUMN email_cap_per_week integer NOT NULL DEFAULT 3 CHECK (email_cap_per_week BETWEEN 0 AND 500),
    ADD COLUMN email_quiet_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN email_quiet_start smallint NOT NULL DEFAULT 20 CHECK (email_quiet_start BETWEEN 0 AND 23),
    ADD COLUMN email_quiet_end smallint NOT NULL DEFAULT 8 CHECK (email_quiet_end BETWEEN 0 AND 23),
    ADD COLUMN email_time_zone text NOT NULL DEFAULT 'UTC' CHECK (length(email_time_zone) <= 100);

