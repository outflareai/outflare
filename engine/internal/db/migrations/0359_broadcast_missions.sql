-- The mission a broadcast was made in, so the revenue credited to its
-- campaign link counts toward the mission's revenue_attributed goal, as a
-- sequence's does. Null for broadcasts made outside a mission.
ALTER TABLE email_broadcasts ADD COLUMN mission_id uuid REFERENCES missions (id) ON DELETE SET NULL;

CREATE INDEX email_broadcasts_mission ON email_broadcasts (mission_id) WHERE mission_id IS NOT NULL;
