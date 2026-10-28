-- Existing deals move over (0228): every workspace gets the Deal type,
-- every deal gets its CRM record, and every deal record already made in the
-- CRM (an Attio import, a reply rule, by hand) gets its deals row, so
-- outcomes count it. Both tables are small: a deal is a sales opportunity,
-- not an event.

SELECT outflare_deal_type(id) FROM workspaces;

-- Touching each row runs its trigger, which writes the other side.
UPDATE deals SET updated_at = updated_at;
UPDATE records r SET updated_at = r.updated_at
WHERE r.record_type = 'deal' AND r.archived_at IS NULL AND r.merged_into IS NULL
    AND NOT EXISTS (SELECT 1 FROM deals d WHERE d.id = r.id);
