-- Presence (Coverage renamed in the app): more placement types, organic
-- placements, who delivered a placement, its CRM deal, and the CRM
-- companies and people involved in it.

-- Influencer posts, YouTube videos, forum threads, MCP directories and app
-- store listings join the existing types. Widening a CHECK only expands:
-- the previous version never writes the new values.
ALTER TABLE coverage_placements DROP CONSTRAINT IF EXISTS coverage_placements_kind_check;
ALTER TABLE coverage_placements ADD CONSTRAINT coverage_placements_kind_check CHECK (kind IN (
    'blog','comparison','news','profile','directory','review','roundup','case_study','partner','integration','resource','interview','podcast','video','community','display_ad','newsletter','sponsorship','custom',
    'influencer','youtube','forum','mcp_directory','app_listing'));

-- Organic: published unprompted and free, as opposed to earned (pitched for).
ALTER TABLE coverage_placements DROP CONSTRAINT IF EXISTS coverage_placements_arrangement_check;
ALTER TABLE coverage_placements ADD CONSTRAINT coverage_placements_arrangement_check CHECK (arrangement IN ('earned','paid','exchange','affiliate','organic','unknown'));

-- Who delivered it (the agency or partner itself is a party below), and the
-- CRM deal the partnerships team works it in.
ALTER TABLE coverage_placements
    ADD COLUMN delivered_by text NOT NULL DEFAULT 'unknown' CHECK (delivered_by IN ('unknown','in_house','agency','partner')),
    ADD COLUMN deal_id uuid REFERENCES deals(id) ON DELETE SET NULL;

-- The CRM companies and people involved in a placement, with their role: the
-- agency or partner that delivered it, the creator, author or editor.
CREATE TABLE coverage_parties (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    version integer NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    placement_id uuid NOT NULL REFERENCES coverage_placements(id) ON DELETE CASCADE,
    record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    role text NOT NULL DEFAULT 'other' CHECK (role IN ('agency','partner','creator','author','editor','owner','account_manager','other')),
    notes text NOT NULL DEFAULT '' CHECK (length(notes) <= 20000)
);
CREATE UNIQUE INDEX coverage_parties_unique_idx ON coverage_parties (workspace_id, brand_id, placement_id, record_id, role);
CREATE INDEX coverage_parties_brand_idx ON coverage_parties (workspace_id, brand_id, created_at DESC, id);
CREATE INDEX coverage_parties_record_idx ON coverage_parties (workspace_id, record_id);
