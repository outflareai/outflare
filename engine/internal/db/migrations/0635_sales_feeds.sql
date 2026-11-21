-- Sales feeds are the Slack channels a brand chose to read: each post becomes a
-- stored item that turns into a person, a Sales conversation or a signal. Posts
-- stay encrypted, and a feed item links to its person only when one is known.
-- A channel is never deleted while its brand lives: removing it sets
-- removed_at and keeps its posts, and a channel whose connection is deleted
-- keeps its posts with no connection until it is saved with another.
CREATE TABLE sales_feed_channels (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 integration_id uuid REFERENCES integrations(id) ON DELETE SET NULL,
 channel_id text NOT NULL CHECK(length(channel_id) BETWEEN 1 AND 40),
 channel_name text NOT NULL DEFAULT '',
 kind text NOT NULL CHECK(kind IN ('new_person','handover','signal','context')),
 signal_type text NOT NULL DEFAULT 'upsell',
 read_back_days integer NOT NULL DEFAULT 3 CHECK(read_back_days BETWEEN 0 AND 7),
 enabled boolean NOT NULL DEFAULT true,
 cursor_ts text NOT NULL DEFAULT '',
 -- Where reading began: when the channel was added or last turned on, minus
 -- its read_back_days. The sweep stores nothing older.
 read_since timestamptz,
 last_read_at timestamptz,
 last_swept_at timestamptz,
 error_code text NOT NULL DEFAULT '',
 removed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,brand_id,channel_id)
);
CREATE INDEX sales_feed_channels_brand ON sales_feed_channels(brand_id);
CREATE INDEX sales_feed_channels_integration ON sales_feed_channels(integration_id);

CREATE TABLE sales_feed_items (
 id uuid PRIMARY KEY,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 feed_channel_id uuid NOT NULL REFERENCES sales_feed_channels(id) ON DELETE CASCADE,
 slack_ts text NOT NULL,
 record_id uuid REFERENCES records(id) ON DELETE CASCADE,
 lead_id uuid REFERENCES sales_leads(id) ON DELETE SET NULL,
 kind text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','read','needs_look','spam','ignored','failed')),
 resolved_by uuid REFERENCES users(id) ON DELETE SET NULL,
 spam_confidence real CHECK(spam_confidence BETWEEN 0 AND 1),
 content_sealed bytea NOT NULL,
 content_hash text NOT NULL,
 latest_reply text NOT NULL DEFAULT '',
 error_code text NOT NULL DEFAULT '',
 deleted_in_slack boolean NOT NULL DEFAULT false,
 occurred_at timestamptz NOT NULL,
 edited_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(feed_channel_id,slack_ts)
);
CREATE INDEX sales_feed_items_record ON sales_feed_items(record_id);
CREATE INDEX sales_feed_items_lead ON sales_feed_items(lead_id);
CREATE INDEX sales_feed_items_status ON sales_feed_items(workspace_id,brand_id,status,occurred_at DESC);
CREATE INDEX sales_feed_items_resolved_by ON sales_feed_items(resolved_by);

ALTER TABLE sales_leads ADD COLUMN feed_kind text, ADD COLUMN feed_channel_id uuid REFERENCES sales_feed_channels(id) ON DELETE SET NULL;
CREATE INDEX sales_leads_feed_kind ON sales_leads(workspace_id,brand_id,feed_kind) WHERE feed_kind IS NOT NULL;
CREATE INDEX sales_leads_feed_channel ON sales_leads(feed_channel_id);

CREATE FUNCTION outflare_sales_feed_record_brand() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id THEN
  UPDATE sales_feed_items SET brand_id=NEW.brand_id WHERE workspace_id=NEW.workspace_id AND record_id=NEW.id;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER sales_feed_record_brand AFTER UPDATE OF brand_id ON records FOR EACH ROW EXECUTE FUNCTION outflare_sales_feed_record_brand();

-- Signals raised from a feed post.
ALTER TABLE signals DROP CONSTRAINT signals_source_check;
ALTER TABLE signals ADD CONSTRAINT signals_source_check CHECK (source IN ('event', 'journey', 'manual', 'agent', 'api', 'feed'));
