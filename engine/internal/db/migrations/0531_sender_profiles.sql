-- A sender's mailbox profile: the name parts, photo and signature its
-- mailboxes use. Mailbox providers and the managed mailbox service apply
-- them; a mailbox may still carry its own deliberate override.
ALTER TABLE senders
    ADD COLUMN first_name text NOT NULL DEFAULT '' CHECK (length(first_name) <= 200),
    ADD COLUMN last_name text NOT NULL DEFAULT '' CHECK (length(last_name) <= 200),
    ADD COLUMN photo_url text NOT NULL DEFAULT '' CHECK (length(photo_url) <= 2048),
    ADD COLUMN signature_html text NOT NULL DEFAULT '' CHECK (length(signature_html) <= 10000);
