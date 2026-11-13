-- A brand's click filter lists (plans/ad-tracker.md, B7b): addresses,
-- ranges and pieces of a user agent its own team knows to be bots or
-- fraud. A redirect click that matches is flagged (fraud reason
-- filter_list) like the other signals: it still goes on, but counts
-- against no cap and shows as suspect in the reports.
CREATE TABLE tracker_click_filters (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('ip', 'user_agent')),
    value        text NOT NULL CHECK (length(value) BETWEEN 1 AND 200),
    note         text NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    created_by   uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, kind, value)
);
CREATE INDEX tracker_click_filters_brand_idx ON tracker_click_filters (workspace_id, brand_id);
