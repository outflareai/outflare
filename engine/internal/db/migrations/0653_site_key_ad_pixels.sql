-- Meta's and TikTok's pixels, loaded by the website script. A site key
-- with one of these on tells its script to load the pixel the brand's
-- conversions already go to, and to give each conversion the page sends
-- the id the server sends it with, so the platform counts it once. Off
-- until someone turns it on.
ALTER TABLE site_keys ADD COLUMN meta_pixel boolean NOT NULL DEFAULT false;
ALTER TABLE site_keys ADD COLUMN tiktok_pixel boolean NOT NULL DEFAULT false;
