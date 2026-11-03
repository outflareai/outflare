-- One workspace switch (plans/customer-data-platform, step 6, the owner's
-- decision of 2026-11-02): when on, only owners and admins change customer
-- records by hand or through their own API tokens. Members and custom
-- roles keep reading them. Imports, syncs and other automatic writes are
-- not affected.
--
-- Expand only (docs/migrations.md): a column with a default.

ALTER TABLE workspaces ADD COLUMN records_edit_owners_only boolean NOT NULL DEFAULT false;
