-- A dashboard tile can keep its insight's own dates instead of following
-- the dashboard's date range, so one report can show the last 12 months
-- on a dashboard that shows the last 30 days.
ALTER TABLE dashboard_tiles ADD COLUMN own_dates boolean NOT NULL DEFAULT false;
