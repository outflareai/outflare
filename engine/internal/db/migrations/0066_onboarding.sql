-- Getting started: the steps a new workspace is walked through (a brand,
-- a first goal, an AI, the tools that goal needs, a first mission, then
-- teammates). Whether a step is done is worked out from the workspace
-- itself; this row only remembers the goal picked, what a person chose to
-- skip and whether they closed the walk-through, so it can be picked up
-- again later.
CREATE TABLE workspace_onboarding (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Step ids a person skipped, such as {team,ai}.
    skipped      text[] NOT NULL DEFAULT '{}',
    -- What the workspace wants to do first (outreach, search, reports,
    -- nurture); it picks the tools and the first mission suggested.
    goal         text,
    -- Set when a person closed the walk-through; the app stops opening it.
    dismissed_at timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL
);
