-- Matching synced Telegram chats to customer records. Outflare looks for
-- the person or company itself (a Telegram id a record already has, a
-- phone number, a username, an email address in the chat), then asks the
-- workspace's own AI, and the account's holder confirms each suggestion in
-- a queue. A confirmed chat gets record_id, and the record gets the chat's
-- Telegram id as an identity, so the next chat with them matches by itself.

-- pending: not looked at yet; suggested: a record waits for a person to
-- confirm; no_match: nothing found, a person picks or creates the record;
-- confirmed: linked (record_id).
ALTER TABLE telegram_chats ADD COLUMN match_status text NOT NULL DEFAULT 'pending'
    CHECK (match_status IN ('pending', 'suggested', 'no_match', 'confirmed'));
ALTER TABLE telegram_chats ADD COLUMN suggested_record_id uuid REFERENCES records (id) ON DELETE SET NULL;
-- What the suggestion rests on: telegram (an id a record already has),
-- phone, username, email (an address in the chat), domain (a company's
-- domain in a group), or ai.
ALTER TABLE telegram_chats ADD COLUMN match_reason text
    CHECK (match_reason IN ('telegram', 'phone', 'username', 'email', 'domain', 'ai'));
ALTER TABLE telegram_chats ADD COLUMN match_confidence real CHECK (match_confidence BETWEEN 0 AND 1);
-- The AI's one-line reason, sealed (it quotes names), bound to the chat id.
ALTER TABLE telegram_chats ADD COLUMN match_note_sealed bytea;
-- The AI's guess that a chat isn't company business, for the person to
-- confirm. Never applied on its own: marking a chat personal removes it.
ALTER TABLE telegram_chats ADD COLUMN suggested_kind text CHECK (suggested_kind IN ('customer', 'personal'));
ALTER TABLE telegram_chats ADD COLUMN matched_at timestamptz;
ALTER TABLE telegram_chats ADD COLUMN matched_by uuid REFERENCES actors (id) ON DELETE SET NULL;

-- Chats there was already history for start as pending too.
CREATE INDEX telegram_chats_match_idx ON telegram_chats (workspace_id, brand_id, match_status)
    WHERE match_status IN ('pending', 'suggested', 'no_match');
CREATE INDEX telegram_chats_suggested_idx ON telegram_chats (suggested_record_id) WHERE suggested_record_id IS NOT NULL;

-- Customers waiting for an answer: waiting_since is the first message from
-- them since the company last wrote (service messages, like someone
-- joining, don't count), and waiting_alerted_at when the account's holder
-- was told.
ALTER TABLE telegram_messages ADD COLUMN service boolean NOT NULL DEFAULT false;
ALTER TABLE telegram_chats ADD COLUMN waiting_since timestamptz;
ALTER TABLE telegram_chats ADD COLUMN waiting_alerted_at timestamptz;
CREATE INDEX telegram_chats_waiting_idx ON telegram_chats (waiting_since) WHERE waiting_since IS NOT NULL;

-- The AI's summary of a chat, sealed and bound to the chat id (it quotes
-- the chat), when it was written and the newest message it read.
ALTER TABLE telegram_chats ADD COLUMN summary_sealed bytea;
ALTER TABLE telegram_chats ADD COLUMN summary_at timestamptz;
ALTER TABLE telegram_chats ADD COLUMN summary_through integer;
