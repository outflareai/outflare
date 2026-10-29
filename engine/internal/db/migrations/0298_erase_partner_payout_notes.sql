-- Whether erasing a partner's record also clears the free-text note and
-- reference on their payouts, which can hold their name or email. One
-- switch for every program in the workspace. The amounts, dates and status
-- always stay.
ALTER TABLE workspaces ADD COLUMN erase_partner_payout_notes boolean NOT NULL DEFAULT true;
