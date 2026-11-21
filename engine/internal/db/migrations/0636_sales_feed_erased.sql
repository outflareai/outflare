-- A Slack post whose stored copy went when its person or company was erased.
-- Reading a channel never stores a post with a marker again, so the poll
-- and the sweep can't bring an erased person back. It holds only where the
-- post was (its channel and Slack timestamp): nothing about the person.
CREATE TABLE sales_feed_erased (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 feed_channel_id uuid NOT NULL REFERENCES sales_feed_channels(id) ON DELETE CASCADE,
 slack_ts text NOT NULL,
 erased_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(feed_channel_id,slack_ts)
);
CREATE INDEX sales_feed_erased_workspace ON sales_feed_erased(workspace_id);
CREATE INDEX sales_feed_erased_brand ON sales_feed_erased(brand_id);
