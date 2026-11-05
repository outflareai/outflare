-- Keywords (step 3 of the paid ads plan): Outflare can add a keyword to
-- an ad group, add a negative keyword, and give a keyword its own ad group
-- on Google Ads, as changes a person approves; and suggest them.
--
-- Expand only (docs/migrations.md): each kind CHECK takes more values. The
-- previous version writes only the old ones.

ALTER TABLE ad_changes DROP CONSTRAINT ad_changes_kind_check;
ALTER TABLE ad_changes ADD CONSTRAINT ad_changes_kind_check
    CHECK (kind IN ('pause', 'resume', 'budget', 'keyword_add', 'negative_add', 'keyword_split'));

ALTER TABLE ad_suggestions DROP CONSTRAINT ad_suggestions_kind_check;
ALTER TABLE ad_suggestions ADD CONSTRAINT ad_suggestions_kind_check
    CHECK (kind IN ('budget_up', 'budget_down', 'pause', 'resume', 'keyword_add', 'negative_add', 'keyword_split'));
