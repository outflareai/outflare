-- A negotiated proposal is assigned to one customer-backed partner, never claimable.
CREATE TABLE partner_proposals (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid REFERENCES brands(id) ON DELETE CASCADE,
 program_id uuid NOT NULL REFERENCES partner_programs(id) ON DELETE CASCADE,
 membership_id uuid NOT NULL REFERENCES partner_memberships(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 status text NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','quote_requested','quote_received','offered','accepted','declined','completed')),
 version integer NOT NULL DEFAULT 1,
 config jsonb NOT NULL,
 progress jsonb NOT NULL DEFAULT '{}',
 invitation_hash bytea,
 accepted_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_proposals_program_idx ON partner_proposals(workspace_id,brand_id,program_id,status);
CREATE INDEX partner_proposals_record_idx ON partner_proposals(workspace_id,record_id);
