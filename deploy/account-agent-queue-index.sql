-- Additive performance index; compatible with older executables and schema v25.
-- Retain on executable rollback. Does not rewrite rows or component markers.
SET SESSION lock_wait_timeout = 30;
ALTER TABLE metatrash_agent_jobs
    ADD INDEX IF NOT EXISTS metatrash_agent_jobs_queue_space (status, space_id, queued_at, job_id);
