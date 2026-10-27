-- Mailbox allocation: which mailboxes send for which sequence, decided by
-- people, by the allocator, or both. Hard rules live in the schema where
-- they can: one current assignment per mailbox, one lock per mailbox.

-- Suggest: the allocator proposes changes and a person applies them.
-- Automatic: routine changes apply on their own, logged with undo.
ALTER TABLE workspaces ADD COLUMN allocation_mode text NOT NULL DEFAULT 'automatic'
    CHECK (allocation_mode IN ('suggest', 'automatic'));

CREATE TABLE teams (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL,
    -- Null follows the workspace.
    allocation_mode text CHECK (allocation_mode IN ('suggest', 'automatic')),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX teams_name_idx ON teams (workspace_id, lower(name));

-- People in a team: who may approve lending the team's mailboxes.
CREATE TABLE team_members (
    team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (team_id, user_id)
);

-- Who a mailbox sends as: a real person, a persona, or a shared identity
-- any team may use. Separate from users, since most senders never log in.
CREATE TABLE senders (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL,
    kind         text NOT NULL DEFAULT 'person' CHECK (kind IN ('person', 'persona', 'shared')),
    user_id      uuid REFERENCES users (id) ON DELETE SET NULL,
    -- retired: left the company or no longer used. Its mailboxes take no
    -- new leads and finish their follow-ups.
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX senders_name_idx ON senders (workspace_id, lower(name));

CREATE TABLE sender_teams (
    sender_id uuid NOT NULL REFERENCES senders (id) ON DELETE CASCADE,
    team_id   uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    PRIMARY KEY (sender_id, team_id)
);

-- A mailbox's sender is only used for matching once a person or agent
-- confirms it. suggested_sender_id is a guess waiting for that.
ALTER TABLE mailboxes
    ADD COLUMN sender_id           uuid REFERENCES senders (id) ON DELETE SET NULL,
    ADD COLUMN sender_confirmed_at timestamptz,
    ADD COLUMN sender_confirmed_by uuid REFERENCES actors (id),
    ADD COLUMN suggested_sender_id uuid REFERENCES senders (id) ON DELETE SET NULL;

-- Sequences are the campaigns mailboxes are allocated to.
ALTER TABLE sequences
    ADD COLUMN team_id         uuid REFERENCES teams (id) ON DELETE SET NULL,
    -- 0 low, 1 normal, 2 high. Higher may drain lower down to its minimum.
    ADD COLUMN priority        smallint NOT NULL DEFAULT 1 CHECK (priority BETWEEN 0 AND 2),
    -- Sends a day the sequence should have capacity for. Null: daily
    -- new-lead limit times the number of steps, or no automatic allocation.
    ADD COLUMN target_daily    integer CHECK (target_daily BETWEEN 1 AND 1000000),
    -- Never drained below this many sends a day of capacity.
    ADD COLUMN min_daily       integer CHECK (min_daily BETWEEN 1 AND 1000000),
    -- Null follows the team, then the workspace.
    ADD COLUMN allocation_mode text CHECK (allocation_mode IN ('suggest', 'automatic')),
    ADD COLUMN created_by      uuid REFERENCES actors (id);

-- Preferred senders for a sequence, picked before the rest of its team.
CREATE TABLE sequence_senders (
    sequence_id uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    sender_id   uuid NOT NULL REFERENCES senders (id) ON DELETE CASCADE,
    PRIMARY KEY (sequence_id, sender_id)
);

-- Where each mailbox sends now. The primary key is the rule that a mailbox
-- serves one sequence at a time. draining: takes no new leads, finishes
-- the follow-ups already started from it, then moves to next_sequence_id
-- (or becomes free).
CREATE TABLE mailbox_assignments (
    mailbox_id       uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    sequence_id      uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    state            text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'draining')),
    next_sequence_id uuid REFERENCES sequences (id) ON DELETE SET NULL,
    -- manual: a person or API call. automatic: the allocator.
    source           text NOT NULL CHECK (source IN ('manual', 'automatic')),
    reason           text NOT NULL DEFAULT '',
    -- Bumped on every change, so a change prepared against an old state fails.
    version          integer NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mailbox_assignments_seq_idx ON mailbox_assignments (sequence_id, state);
CREATE INDEX mailbox_assignments_ws_idx ON mailbox_assignments (workspace_id);

-- What a person told the allocator about a mailbox. One lock per mailbox,
-- so locks never contradict each other.
--   sequence:    only this sequence, never moved by the allocator.
--   team:        only sequences owned by this team.
--   manual_only: the allocator never touches it.
CREATE TABLE mailbox_locks (
    mailbox_id   uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('sequence', 'team', 'manual_only')),
    sequence_id  uuid REFERENCES sequences (id) ON DELETE CASCADE,
    team_id      uuid REFERENCES teams (id) ON DELETE CASCADE,
    until        timestamptz,
    note         text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors (id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((kind = 'sequence') = (sequence_id IS NOT NULL)),
    CHECK ((kind = 'team') = (team_id IS NOT NULL))
);

-- Every assignment change, for the activity list, explanations and undo.
CREATE TABLE allocation_changes (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    mailbox_id       uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    -- assign, release, drain, move, finish_drain
    action           text NOT NULL,
    from_sequence_id uuid REFERENCES sequences (id) ON DELETE SET NULL,
    to_sequence_id   uuid REFERENCES sequences (id) ON DELETE SET NULL,
    source           text NOT NULL CHECK (source IN ('manual', 'automatic', 'approval', 'undo')),
    reason           text NOT NULL DEFAULT '',
    actor_id         uuid REFERENCES actors (id),
    -- The assignment version this change produced; undo needs it unchanged.
    version          integer,
    undone_at        timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX allocation_changes_ws_idx ON allocation_changes (workspace_id, created_at DESC);
CREATE INDEX allocation_changes_seq_idx ON allocation_changes (to_sequence_id, created_at DESC);
CREATE INDEX allocation_changes_from_idx ON allocation_changes (from_sequence_id, created_at DESC);

-- The allocator's latest proposals, replaced on every run. In Suggest
-- mode they wait here for a person; borrow proposals always do.
CREATE TABLE allocation_suggestions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    sequence_id      uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    mailbox_id       uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    action           text NOT NULL CHECK (action IN ('assign', 'release', 'drain')),
    from_sequence_id uuid REFERENCES sequences (id) ON DELETE CASCADE,
    -- borrow: the mailbox's sender belongs to another team, which must agree.
    needs            text NOT NULL DEFAULT 'review' CHECK (needs IN ('review', 'team_approval', 'owner_approval')),
    approver_team_id uuid REFERENCES teams (id) ON DELETE CASCADE,
    reason           text NOT NULL,
    -- Assignment version the proposal was made against; 0 when free.
    base_version     integer NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX allocation_suggestions_ws_idx ON allocation_suggestions (workspace_id, sequence_id);

-- Each sequence's capacity after the last run: what it needs, what it has
-- and, when short, why nothing else could be used.
CREATE TABLE allocation_status (
    sequence_id  uuid PRIMARY KEY REFERENCES sequences (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    needed_daily integer,
    active_daily integer NOT NULL DEFAULT 0,
    -- [{reason, count}] for mailboxes that were not used
    blocking     jsonb NOT NULL DEFAULT '[]',
    note         text NOT NULL DEFAULT '',
    planned_at   timestamptz NOT NULL DEFAULT now()
);

-- Agents run on tokens; their limits live on the token.
ALTER TABLE api_tokens
    ADD COLUMN daily_send_budget  integer CHECK (daily_send_budget BETWEEN 1 AND 1000000),
    ADD COLUMN max_priority       smallint CHECK (max_priority BETWEEN 0 AND 2),
    ADD COLUMN may_take_mailboxes boolean NOT NULL DEFAULT false,
    ADD COLUMN default_team_id    uuid REFERENCES teams (id) ON DELETE SET NULL;

-- Follow-ups waiting on a mailbox that is gone. Found by the allocator,
-- resolved by a person: cancel them, or send them as fresh emails from
-- another of the sequence's mailboxes.
CREATE INDEX enrollments_orphaned_idx ON enrollments (sequence_id)
    WHERE status = 'active' AND mailbox_id IS NULL AND next_step > 0;
CREATE INDEX enrollments_mailbox_active_idx ON enrollments (mailbox_id) WHERE status = 'active';

-- Mailboxes already used by exactly one live sequence through its pool
-- start out assigned to it, so nothing stops sending on upgrade. Others
-- are left free for the allocator or a person.
INSERT INTO mailbox_assignments (mailbox_id, workspace_id, sequence_id, source, reason)
SELECT m.id, m.workspace_id, (array_agg(s.id))[1], 'manual', 'Assigned from the sequence''s mailbox pool on upgrade.'
FROM mailboxes m
JOIN sequences s ON s.workspace_id = m.workspace_id AND s.status IN ('active', 'paused')
     AND (s.pool_id = m.pool_id OR s.pool_id IS NULL)
GROUP BY m.id, m.workspace_id
HAVING count(*) = 1;

-- Set when a person chose to send the remaining follow-ups of a lead whose
-- mailbox is gone from another mailbox, as a fresh email.
ALTER TABLE enrollments ADD COLUMN handoff boolean NOT NULL DEFAULT false;

-- Proposals a person turned down. The allocator does not make them again
-- until someone clears the decline.
CREATE TABLE allocation_declines (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    mailbox_id   uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    sequence_id  uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    declined_by  uuid REFERENCES actors (id),
    declined_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mailbox_id, sequence_id)
);
