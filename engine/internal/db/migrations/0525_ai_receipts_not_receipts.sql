-- AI receipts read any email an AI company signed as a receipt, so sign-in
-- alerts and newsletters a Google Workspace rule copied were kept, some
-- matched to an account. The ones that carry nothing a receipt says (no
-- amount, plan, seats or period) are set aside as ignored.
UPDATE ai_receipts SET status = 'ignored'
WHERE status IN ('matched', 'needs_review')
    AND amount_minor IS NULL AND plan IS NULL AND seats IS NULL AND period_end IS NULL;

-- Mail a Workspace rule copied for a colleague was credited to the member
-- whose domain let it in. It names no member: forwarded_by is cleared when
-- the address it came through is neither that member's own nor one they
-- linked.
UPDATE ai_receipts r SET forwarded_by = NULL
FROM inbound_emails e
WHERE e.id = r.inbound_email_id AND r.forwarded_by IS NOT NULL AND e.forwarded_by IS NOT NULL
    AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = r.forwarded_by AND lower(u.email) = e.forwarded_by)
    AND NOT EXISTS (SELECT 1 FROM inbound_forwarders f
        WHERE f.workspace_id = r.workspace_id AND f.user_id = r.forwarded_by AND f.email = e.forwarded_by);
