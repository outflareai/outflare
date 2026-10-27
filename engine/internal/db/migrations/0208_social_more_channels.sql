-- Bluesky, Threads, Mastodon, DEV and Docker Hub profiles in a brand's
-- links are read every day like the other public profiles. Only widens
-- the check, so the version before still works.
ALTER TABLE social_accounts DROP CONSTRAINT social_accounts_channel_check;
ALTER TABLE social_accounts ADD CONSTRAINT social_accounts_channel_check
    CHECK (channel IN ('reddit', 'x', 'linkedin', 'youtube', 'instagram', 'tiktok', 'facebook', 'github', 'discord',
                       'bluesky', 'threads', 'mastodon', 'dev_to', 'docker_hub'));
