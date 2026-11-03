-- Audience transitions (plans/customer-data-platform, step 3): who joined
-- and left each smart or static list, and when; and smart lists of
-- companies as well as people.

-- Every time a record joined or left a list. Refreshes and static list
-- changes write it in the transaction that changed the members. A
-- deleted record (Recently deleted) keeps its place, so it neither leaves
-- nor joins again.
CREATE TABLE smart_list_changes (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    list_id       uuid NOT NULL REFERENCES smart_lists (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    change        text NOT NULL CHECK (change IN ('enter', 'leave')),
    at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX smart_list_changes_list_idx ON smart_list_changes (list_id, at DESC, id DESC);
CREATE INDEX smart_list_changes_record_idx ON smart_list_changes (workspace_id, record_id, at DESC);

-- What a list holds: people (the default, as before) or companies. Only a
-- smart list can hold companies; it can't change once made.
ALTER TABLE smart_lists ADD COLUMN record_type text NOT NULL DEFAULT 'person' CHECK (record_type IN ('person', 'account'));

-- Query (docs/query.md): the history, and what each list holds.
CREATE VIEW query.smart_list_changes WITH (security_barrier) AS
SELECT c.id, c.list_id, c.record_id, c.brand_id, c.change, c.at
FROM smart_list_changes c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);
