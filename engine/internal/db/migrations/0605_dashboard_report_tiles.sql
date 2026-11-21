-- A dashboard tile can show a saved report (plans/ad-tracker-ui.md, B7): a
-- report view of the drill-down report, pinned beside the insights. A tile
-- is then a saved insight, a saved report or a note. Removing the view
-- removes its tiles, as removing an insight does. Only adds: the check is
-- widened to let a report tile have no insight and no note, and a version
-- that doesn't know the column shows such a tile as an empty note.
ALTER TABLE dashboard_tiles ADD COLUMN report_view_id uuid REFERENCES report_views (id) ON DELETE CASCADE;
CREATE INDEX dashboard_tiles_report_view_idx ON dashboard_tiles (report_view_id) WHERE report_view_id IS NOT NULL;
ALTER TABLE dashboard_tiles DROP CONSTRAINT dashboard_tiles_check;
ALTER TABLE dashboard_tiles ADD CONSTRAINT dashboard_tiles_check CHECK (insight_id IS NOT NULL OR report_view_id IS NOT NULL OR note <> '');
