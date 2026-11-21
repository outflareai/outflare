-- Autocapture: whether the website script records clicks on buttons and
-- links (element_clicked) and form submits (form_submitted) on its own,
-- with no code on the site. Each is a switch per site key, off by default,
-- so nothing new is collected until someone turns it on. The script never
-- reads what a visitor typed.
ALTER TABLE site_keys ADD COLUMN capture_clicks boolean NOT NULL DEFAULT false;
ALTER TABLE site_keys ADD COLUMN capture_forms boolean NOT NULL DEFAULT false;
