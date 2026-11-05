-- Before workers claimed only the job kinds they have a handler for, a
-- worker of an older version running beside a newer one during a deploy
-- (or after a rollback) could claim a customers.link job, find no handler
-- and mark it dead at once. Those workspaces' imported leads and older
-- unsubscribes were then never linked to their person records, and the
-- jobs' idempotency keys stopped the migrations that enqueued them from
-- ever running them again. Put them back in the queue as new: the link
-- job only fills in missing links, so running it again is safe.

UPDATE jobs
SET status = 'queued', attempts = 0, locked_by = NULL, locked_at = NULL,
    last_error = 'requeued by migration 0429', run_at = now(), updated_at = now()
WHERE kind = 'customers.link' AND status = 'dead'
  AND last_error LIKE 'permanent job failure: no handler for kind%';
