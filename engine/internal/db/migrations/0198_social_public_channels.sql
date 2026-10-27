-- Social accounts on every channel a brand's public profile shows a
-- follower count for, read every day through the workspace's own page
-- reader (Geonode): YouTube, Instagram, TikTok, Facebook, GitHub and
-- Discord beside X, LinkedIn and Reddit. Only widens the check, so the
-- version before still works.
ALTER TABLE social_accounts DROP CONSTRAINT social_accounts_channel_check;
ALTER TABLE social_accounts ADD CONSTRAINT social_accounts_channel_check
    CHECK (channel IN ('reddit', 'x', 'linkedin', 'youtube', 'instagram', 'tiktok', 'facebook', 'github', 'discord'));
