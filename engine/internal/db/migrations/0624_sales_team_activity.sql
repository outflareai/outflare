-- Team reports and the activity feed scan a bounded time range inside one brand.
CREATE INDEX sales_events_team_time ON sales_events(workspace_id,brand_id,occurred_at DESC,id DESC);
CREATE INDEX sales_events_team_actor_time ON sales_events(workspace_id,brand_id,actor_id,occurred_at DESC,id DESC);
CREATE INDEX audit_events_sales_seen_time ON audit_events(workspace_id,brand_id,created_at DESC,id DESC) WHERE action='sales.lead_seen';
