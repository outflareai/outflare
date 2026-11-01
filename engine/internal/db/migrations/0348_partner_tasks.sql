-- Task opportunities belong to the program's brand; partner people remain records.
CREATE TABLE partner_audiences (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid REFERENCES brands(id) ON DELETE CASCADE,
 program_id uuid NOT NULL REFERENCES partner_programs(id) ON DELETE CASCADE,
 membership_id uuid NOT NULL REFERENCES partner_memberships(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 url text NOT NULL, host text NOT NULL, kind text NOT NULL, role text NOT NULL,
 topics text NOT NULL DEFAULT '', reach bigint NOT NULL DEFAULT 0 CHECK (reach >= 0),
 reach_source text NOT NULL DEFAULT 'self_reported', measured_at timestamptz,
 challenge text NOT NULL DEFAULT replace(gen_random_uuid()::text || gen_random_uuid()::text, '-', ''),
 verified_at timestamptz, last_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(membership_id, url)
);
CREATE INDEX partner_audiences_program_idx ON partner_audiences(workspace_id, brand_id, program_id);
CREATE INDEX partner_audiences_record_idx ON partner_audiences(workspace_id, record_id);
CREATE TABLE partner_tasks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid REFERENCES brands(id) ON DELETE CASCADE,
 program_id uuid NOT NULL REFERENCES partner_programs(id) ON DELETE CASCADE,
 status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused')),
 config jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_tasks_program_idx ON partner_tasks(workspace_id, brand_id, program_id);
CREATE TABLE partner_task_submissions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid REFERENCES brands(id) ON DELETE CASCADE,
 program_id uuid NOT NULL REFERENCES partner_programs(id) ON DELETE CASCADE,
 task_id uuid NOT NULL REFERENCES partner_tasks(id) ON DELETE CASCADE,
 membership_id uuid NOT NULL REFERENCES partner_memberships(id) ON DELETE CASCADE,
 record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
 audience_id uuid REFERENCES partner_audiences(id) ON DELETE SET NULL,
 status text NOT NULL CHECK (status IN ('applied','offered','accepted','submitted','approved','rejected')),
 amount_cents bigint NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
 evidence_url text NOT NULL DEFAULT '', note text NOT NULL DEFAULT '',
 check_status text NOT NULL DEFAULT '', hold_until timestamptz,
 reward_id uuid REFERENCES partner_rewards(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(task_id, membership_id)
);
CREATE INDEX partner_task_submissions_program_idx ON partner_task_submissions(workspace_id, brand_id, program_id, status);

CREATE INDEX partner_task_submissions_record_idx ON partner_task_submissions(workspace_id, record_id);
