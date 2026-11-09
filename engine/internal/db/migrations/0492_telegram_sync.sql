-- Syncing company Telegram accounts into a brand's CRM. Each signed-in
-- account (telegram_sessions) is pointed at one brand, and its chats and
-- messages are copied into that brand, read only: nothing is sent, and
-- messages stay unread on the account's phone.
--
-- Names, usernames, phone numbers and message text are sealed with the
-- server key, like CRM mail (crm_mail_messages). Chats with colleagues
-- (another account in the team directory) are kept as internal and their
-- messages are never copied.

ALTER TABLE telegram_sessions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
-- off: not syncing; importing: copying history; live: caught up, reading
-- new messages every few minutes; paused: stopped by someone, kept.
ALTER TABLE telegram_sessions ADD COLUMN sync_status text NOT NULL DEFAULT 'off'
    CHECK (sync_status IN ('off', 'importing', 'live', 'paused'));
-- How far back the history import goes, in days.
ALTER TABLE telegram_sessions ADD COLUMN history_days integer NOT NULL DEFAULT 365
    CHECK (history_days BETWEEN 0 AND 3650);
ALTER TABLE telegram_sessions ADD COLUMN sync_started_at timestamptz;
ALTER TABLE telegram_sessions ADD COLUMN synced_at timestamptz;
-- Why the last sync failed (unreachable, signed_out, no_app, bad_app,
-- slowed_down), cleared by the next one that works.
ALTER TABLE telegram_sessions ADD COLUMN sync_error text;
-- Held while a sync runs, so only one runs per account.
ALTER TABLE telegram_sessions ADD COLUMN sync_lease_until timestamptz;

CREATE INDEX telegram_sessions_sync_idx ON telegram_sessions (synced_at) WHERE sync_status IN ('importing', 'live');

-- A chat of a company account: a person, a group or a supergroup.
CREATE TABLE telegram_chats (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    account_id        uuid NOT NULL REFERENCES staff_accounts (id) ON DELETE CASCADE,
    -- user: a private chat; group or channel (a supergroup): a group chat.
    peer_kind         text NOT NULL CHECK (peer_kind IN ('user', 'group', 'channel')),
    -- Telegram's own id for the person or group.
    peer_id           bigint NOT NULL,
    -- Sealed JSON: title, username, phone, access hash, member count.
    details_sealed    bytea NOT NULL,
    -- The customer record it belongs to, once matched.
    record_id         uuid REFERENCES records (id) ON DELETE SET NULL,
    -- unsorted: not looked at yet; customer: a customer chat; internal: a
    -- colleague; personal: not company business, messages not kept.
    kind              text NOT NULL DEFAULT 'unsorted' CHECK (kind IN ('unsorted', 'customer', 'internal', 'personal')),
    -- Only the account's holder (and members.manage) can read it.
    private           boolean NOT NULL DEFAULT false,
    -- Highest and lowest Telegram message ids copied, and whether the
    -- history import reached as far back as it goes.
    last_message_id   integer NOT NULL DEFAULT 0,
    oldest_message_id integer NOT NULL DEFAULT 0,
    history_done      boolean NOT NULL DEFAULT false,
    message_count     integer NOT NULL DEFAULT 0,
    last_message_at   timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX telegram_chats_peer_idx ON telegram_chats (account_id, peer_kind, peer_id);
CREATE INDEX telegram_chats_brand_idx ON telegram_chats (workspace_id, brand_id, last_message_at DESC);
CREATE INDEX telegram_chats_record_idx ON telegram_chats (record_id) WHERE record_id IS NOT NULL;

-- A message in a synced chat. Files aren't copied, only what they are.
CREATE TABLE telegram_messages (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    chat_id      uuid NOT NULL REFERENCES telegram_chats (id) ON DELETE CASCADE,
    message_id   integer NOT NULL,
    -- Sent by the company account, or received.
    outgoing     boolean NOT NULL,
    -- Telegram's id for who wrote it (in groups, any member).
    sender_id    bigint,
    -- Sealed JSON: text, sender's name, media kind, file name, reply.
    content_sealed bytea NOT NULL,
    sent_at      timestamptz NOT NULL,
    edited_at    timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX telegram_messages_chat_idx ON telegram_messages (chat_id, message_id);
CREATE INDEX telegram_messages_brand_idx ON telegram_messages (workspace_id, brand_id, sent_at DESC);
