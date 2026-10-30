-- Events, kept (plans/brands/README.md): POST /v1/brands/{brand}/events
-- saves every event as sent, answers 202 with a receipt per event, and a
-- job processes them in the background. Each row keeps what happened to
-- it: accepted, duplicate, rejected (with a code and what to fix) or held
-- (a name the catalog hasn't approved yet). Rejected and held rows can be
-- fixed and replayed. Accepted and duplicate rows are kept 30 days, rejected
-- and held 90 (purge_after, set when a row reaches its status).
--
-- brand_id is set by the code from the route's brand, never by a trigger
-- and never from the event's body.
CREATE TABLE event_inbox (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    received_at       timestamptz NOT NULL DEFAULT now(),
    -- The request that sent it: one batch per call.
    batch_id          uuid NOT NULL,
    -- Its place in the batch, so a batch is processed in the order sent.
    position          integer NOT NULL DEFAULT 0,
    source            text NOT NULL,
    message_id        text,
    -- The event as sent, with the batch's shared context merged in.
    raw               jsonb NOT NULL,
    status            text NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'accepted', 'duplicate', 'rejected', 'held')),
    code              text,
    reason            text,
    -- The catalog name, once processing worked it out.
    name              text,
    record_id         uuid REFERENCES records (id) ON DELETE SET NULL,
    customer_event_id uuid REFERENCES customer_events (id) ON DELETE SET NULL,
    -- Processing attempts that failed for a reason other than the event.
    attempts          integer NOT NULL DEFAULT 0,
    processed_at      timestamptz,
    request_id        text,
    -- Who sent it, so the processing job records the change as theirs.
    token_id          uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    actor_id          uuid REFERENCES actors (id) ON DELETE SET NULL,
    actor_type        text NOT NULL DEFAULT 'system',
    user_id           uuid REFERENCES users (id) ON DELETE SET NULL,
    purge_after       timestamptz
);

-- The job takes a brand's pending rows in the order they arrived.
CREATE INDEX event_inbox_pending_idx ON event_inbox (brand_id, received_at, batch_id, position) WHERE status = 'pending';
-- The Events page: a brand's events by status, newest first.
CREATE INDEX event_inbox_brand_status_idx ON event_inbox (brand_id, status, received_at DESC, id DESC);
CREATE INDEX event_inbox_brand_recent_idx ON event_inbox (brand_id, received_at DESC, id DESC);
CREATE INDEX event_inbox_batch_idx ON event_inbox (brand_id, batch_id);
-- A retry with the same messageId.
CREATE INDEX event_inbox_message_idx ON event_inbox (brand_id, message_id) WHERE message_id IS NOT NULL;
-- The retention sweep.
CREATE INDEX event_inbox_purge_idx ON event_inbox (purge_after) WHERE purge_after IS NOT NULL;
-- The oldest pending event across every brand, for the monitoring gauge.
CREATE INDEX event_inbox_pending_age_idx ON event_inbox (received_at) WHERE status = 'pending';
