-- Missions can aim at revenue, in whole US dollars: revenue_attributed is
-- payments credited to the campaigns of the mission's sequences under the
-- workspace's default model; revenue_deals is won deals tagged with the
-- mission. Only widens the check, so the version before still works.
ALTER TABLE missions DROP CONSTRAINT IF EXISTS missions_goal_metric_check;
ALTER TABLE missions ADD CONSTRAINT missions_goal_metric_check
    CHECK (goal_metric IN ('meetings', 'replies', 'sends', 'leads', 'revenue_attributed', 'revenue_deals'));
