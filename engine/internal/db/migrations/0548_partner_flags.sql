-- The partner fraud review queue (plans/ad-tracker.md, B7d;
-- ideas/fraud-signals.md, "Partner fraud"). Each credited partner
-- conversion is checked for partner signals and the shared fraud reasons
-- of the tracker click that brought the customer. Each one found is a
-- flag; the conversion's rewards wait while a flag on it is open, until
-- someone clears it (they go on as usual) or confirms it (its pending
-- rewards are rejected). Nothing is rejected on its own.
CREATE TABLE partner_flags (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    program_id    uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    conversion_id uuid NOT NULL REFERENCES partner_conversions (id) ON DELETE CASCADE,
    -- One of fraud.PartnerSignals; the engine checks the name, so a new
    -- signal needs no migration.
    signal        text NOT NULL CHECK (length(signal) BETWEEN 1 AND 40),
    -- Counts and thresholds only: never addresses, names or codes.
    detail        jsonb NOT NULL DEFAULT '{}',
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'cleared', 'confirmed')),
    note          text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    resolved_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    resolved_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversion_id, signal)
);
CREATE INDEX partner_flags_queue_idx ON partner_flags (brand_id, status, created_at DESC);
CREATE INDEX partner_flags_ws_idx ON partner_flags (workspace_id);
CREATE INDEX partner_flags_membership_idx ON partner_flags (membership_id, created_at DESC);
CREATE INDEX partner_flags_open_idx ON partner_flags (conversion_id) WHERE status = 'open';

-- A program moved to another brand takes its flags along, as it does the
-- rest of its rows (0430).
CREATE FUNCTION outflare_partner_flags_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE partner_flags SET brand_id = NEW.brand_id WHERE program_id = NEW.id;
    RETURN NEW;
END
$$;
CREATE TRIGGER partner_programs_flags_moved AFTER UPDATE OF brand_id ON partner_programs
    FOR EACH ROW WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id AND NEW.brand_id IS NOT NULL) EXECUTE FUNCTION outflare_partner_flags_moved();
