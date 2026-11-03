-- What a chat visitor said about the support assistant's answer: helpful
-- or not. sent_message_id is the chat message an Auto run sent, so the
-- visitor's thumbs find their run. "Not helpful" hands the conversation to
-- the team.
ALTER TABLE support_assistant_runs ADD COLUMN sent_message_id uuid REFERENCES support_messages (id) ON DELETE SET NULL;
ALTER TABLE support_assistant_runs ADD COLUMN feedback text CHECK (feedback IN ('helpful', 'not_helpful'));
ALTER TABLE support_assistant_runs ADD COLUMN feedback_at timestamptz;
CREATE INDEX support_assistant_runs_sent ON support_assistant_runs (sent_message_id) WHERE sent_message_id IS NOT NULL;
