-- 0091 deleted only the queued warm-up jobs. Delete the ones that were
-- running, or that the old version enqueued or re-queued during the
-- deploy, so none is left to run or be reaped back. The engine closes any
-- straggler quietly (jobs.Retired).
DELETE FROM jobs WHERE kind IN ('warmup.send', 'warmup.check') AND status IN ('queued', 'running');
