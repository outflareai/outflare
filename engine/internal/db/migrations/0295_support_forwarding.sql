-- Support by forwarding: a business forwards its support mailbox to its
-- support address at the inbound domain (inbound_addresses, purpose
-- support), and each customer email becomes part of a conversation.
-- Replies go out through the workspace's email provider, from the
-- address the customer wrote to.

-- The support address a conversation came in on, and the business's own
-- address its replies go out from (where the customer wrote to, such as
-- support@northpeak.example).
ALTER TABLE support_conversations
    ADD COLUMN inbound_address_id uuid REFERENCES inbound_addresses (id) ON DELETE SET NULL,
    ADD COLUMN reply_from text CHECK (length(reply_from) <= 320);

-- The forwarded message an email came from, and the provider email a
-- reply went out as (its delivery status is the email's).
ALTER TABLE support_messages
    ADD COLUMN inbound_email_id uuid REFERENCES inbound_emails (id) ON DELETE SET NULL,
    ADD COLUMN email_id uuid REFERENCES emails (id) ON DELETE SET NULL;
CREATE INDEX support_messages_email ON support_messages (email_id) WHERE email_id IS NOT NULL;

-- Threading for email sent through a provider: the Message-ID it answers
-- and the ones before it, sent as In-Reply-To and References.
ALTER TABLE emails
    ADD COLUMN in_reply_to text CHECK (length(in_reply_to) <= 998),
    ADD COLUMN references_ids text CHECK (length(references_ids) <= 8000);
