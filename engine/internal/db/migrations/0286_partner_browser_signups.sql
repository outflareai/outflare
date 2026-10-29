-- outflare.signup() on a website reports a sign-up from the browser: it
-- creates the lead at once, but pays a sign-up reward only when the program
-- says so, since anyone can call it. Money otherwise comes from the server
-- and billing only.
ALTER TABLE partner_conversions DROP CONSTRAINT partner_conversions_source_check,
    ADD CONSTRAINT partner_conversions_source_check CHECK (source IN ('api', 'postback', 'billing', 'manual', 'import', 'browser'));
ALTER TABLE partner_programs ADD COLUMN pay_browser_signups boolean NOT NULL DEFAULT false;
