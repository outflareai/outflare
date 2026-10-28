-- A monthly AI limit can warn without stopping: owners and admins still
-- get the 80% and 100% emails, but AI calls go on past the limit. Every
-- limit stops calls by default, as before.
--
-- limit_warn_only is for the workspace's own limit (monthly_limit_cents);
-- warn_only_tasks lists the tools whose own limit (task_limits) only warns.
ALTER TABLE workspace_ai_settings ADD COLUMN limit_warn_only boolean NOT NULL DEFAULT false;
ALTER TABLE workspace_ai_settings ADD COLUMN warn_only_tasks text[] NOT NULL DEFAULT '{}';
