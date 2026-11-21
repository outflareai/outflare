-- Ad pixels loaded by the website script. A site key with this on tells
-- its script to load the ChatGPT Ads pixel of the brand's own ChatGPT Ads
-- connection and to hand it the signed-in visitor's hashed email and id,
-- so the site needs no pixel code of its own. Off until someone turns it
-- on.
ALTER TABLE site_keys ADD COLUMN chatgpt_pixel boolean NOT NULL DEFAULT false;
