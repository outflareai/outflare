-- Missions can aim at revenue partners refer: credited partner sales less
-- refunds, in whole US dollars. Only widens the check, so the version
-- before still works.
ALTER TABLE missions DROP CONSTRAINT IF EXISTS missions_goal_metric_check;
ALTER TABLE missions ADD CONSTRAINT missions_goal_metric_check
    CHECK (goal_metric IN ('meetings', 'replies', 'sends', 'leads', 'revenue_attributed', 'revenue_deals', 'revenue_referred'));
