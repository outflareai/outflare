-- Inbox basics: what a reply means, who handles it, and when it needs
-- looking at again.

ALTER TABLE inbound_messages
    -- What the reply says. Set by the built-in rules on arrival, or by a
    -- person or agent. NULL means nobody has decided yet.
    ADD COLUMN category text CHECK (category IN ('interested', 'meeting_booked', 'question', 'referral',
        'not_now', 'not_interested', 'wrong_person', 'out_of_office', 'unsubscribe')),
    ADD COLUMN category_source text CHECK (category_source IN ('rules', 'human', 'api_token')),
    ADD COLUMN assigned_to uuid REFERENCES users (id) ON DELETE SET NULL,
    -- Hidden from the open inbox until then.
    ADD COLUMN snoozed_until timestamptz,
    -- Handled: hidden from the open inbox until the lead writes again.
    ADD COLUMN done boolean NOT NULL DEFAULT false,
    ADD COLUMN labels text[] NOT NULL DEFAULT '{}';

CREATE INDEX inbound_category_idx ON inbound_messages (workspace_id, category);
CREATE INDEX inbound_assigned_idx ON inbound_messages (workspace_id, assigned_to);
