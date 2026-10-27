-- The analytics copy of product events (docs/adr/0010-analytics-storage.md).
-- When ClickHouse is set up, a mirror copies every new or changed row of
-- customer_events to it, and every deleted one as a tombstone. Postgres
-- stays the source of truth; without ClickHouse these only cost a column,
-- a trigger and an empty table.

-- When a row last changed: its insert, or a merge or record deletion that
-- moved its record_id. The mirror reads rows in (updated_at, id) order.
ALTER TABLE customer_events ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX customer_events_updated_idx ON customer_events (updated_at, id);

CREATE FUNCTION customer_events_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END $$;

CREATE TRIGGER customer_events_touch BEFORE UPDATE ON customer_events
    FOR EACH ROW EXECUTE FUNCTION customer_events_touch();

-- Deleted events, until the mirror has told ClickHouse. The workspace
-- column is from_workspace_id, with no foreign key, on purpose: a deleted
-- workspace's events must still be removed there, so these rows outlive
-- it until the mirror has sent them (then they are deleted). Archiving old
-- events sets outflare.archiving, so ClickHouse keeps what Postgres moved
-- to object storage.
CREATE TABLE customer_event_deletions (
    id           uuid NOT NULL,
    from_workspace_id uuid NOT NULL,
    occurred_at  timestamptz NOT NULL,
    name         text NOT NULL,
    deleted_at   timestamptz NOT NULL DEFAULT now(),
    seq          bigserial PRIMARY KEY
);

CREATE FUNCTION customer_events_deleted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF coalesce(current_setting('outflare.archiving', true), '') <> 'on'
        AND EXISTS (SELECT 1 FROM event_mirror) THEN
        INSERT INTO customer_event_deletions (id, from_workspace_id, occurred_at, name)
        VALUES (OLD.id, OLD.workspace_id, OLD.occurred_at, OLD.name);
    END IF;
    RETURN OLD;
END $$;

CREATE TRIGGER customer_events_deleted AFTER DELETE ON customer_events
    FOR EACH ROW EXECUTE FUNCTION customer_events_deleted();

-- Where each mirror has got to. A row exists once a mirror has started, so
-- installs without ClickHouse record no deletions.
CREATE TABLE event_mirror (
    store        text PRIMARY KEY,
    cursor_at    timestamptz,
    cursor_id    uuid,
    rows_copied  bigint NOT NULL DEFAULT 0,
    last_run_at  timestamptz,
    last_error   text NOT NULL DEFAULT '',
    started_at   timestamptz NOT NULL DEFAULT now()
);
