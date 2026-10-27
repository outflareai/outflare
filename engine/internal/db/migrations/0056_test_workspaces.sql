-- Test workspaces are the sandbox: mailboxes in them are simulated, so
-- nothing is delivered, verified for real or bought.
ALTER TABLE workspaces ADD COLUMN test boolean NOT NULL DEFAULT false;

ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_provider_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_provider_check CHECK (provider IN ('google', 'microsoft', 'other', 'simulated'));
