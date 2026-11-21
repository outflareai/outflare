-- An error issue can be archived: out of the open list until it happens
-- again, when it opens again without counting as a regression (unlike
-- resolved, which reads "Back again"). Widens the status check; nothing
-- else changes, so the previous version keeps running beside this one.
ALTER TABLE error_issues DROP CONSTRAINT error_issues_status_check;
ALTER TABLE error_issues ADD CONSTRAINT error_issues_status_check CHECK (status IN ('open', 'resolved', 'ignored', 'archived'));
