-- GET /v1/workspaces/{ws}/jobs lists a workspace's jobs newest first.
CREATE INDEX jobs_workspace_idx ON jobs (workspace_id, id) WHERE workspace_id IS NOT NULL;
