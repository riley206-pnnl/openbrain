-- OB-031 one-off data-debt reconciliation.
--
-- MANUAL STEP. This statement is NOT run by the application and is NOT a
-- migration. An operator runs it once, by hand, against the live openbrain
-- database after the OB-031 atomic-supersede fix is deployed. It retires the
-- two stale thoughts that the pre-fix silent-failure path left live, leaving a
-- single live thought for the Sadie voice-canon slot.
--
-- Canon slot after reconciliation:
--   5f81f4c7  canonical Sadie voice canon   KEEP (stays live)
--   f9ebb41e  stale                          retired
--   edfcd7fe  orphan pointer                 retired
--
-- The ids above are the leading 8 characters of each thought's UUID. The
-- statement matches on that prefix. Before running, confirm each prefix
-- resolves to exactly one row:
--
--   SELECT id, is_current, superseded_by, left(content, 60)
--   FROM thoughts
--   WHERE id::text LIKE '5f81f4c7%'
--      OR id::text LIKE 'f9ebb41e%'
--      OR id::text LIKE 'edfcd7fe%';
--
-- Run inside a transaction so you can inspect before COMMIT.
--
-- This statement is idempotent: re-running it leaves the same end state.

BEGIN;

-- Resolve the KEEP thought's full UUID once, then point the two retired
-- thoughts at it. If the KEEP prefix does not resolve to exactly one row the
-- UPDATE affects zero rows and you should stop and investigate.
WITH keep AS (
  SELECT id AS keep_id
  FROM thoughts
  WHERE id::text LIKE '5f81f4c7%'
)
UPDATE thoughts t
SET is_current    = FALSE,
    valid_until   = COALESCE(t.valid_until, NOW()),
    superseded_by = keep.keep_id
FROM keep
WHERE (t.id::text LIKE 'f9ebb41e%' OR t.id::text LIKE 'edfcd7fe%');

-- Defensively re-assert that the KEEP thought is live and unretired, in case a
-- prior half-write touched it.
UPDATE thoughts
SET is_current    = TRUE,
    valid_until   = NULL,
    superseded_by = NULL
WHERE id::text LIKE '5f81f4c7%';

-- Expect: exactly one live thought across the three prefixes (the KEEP row).
-- Inspect, then COMMIT (or ROLLBACK if the count is not 1).
SELECT id, is_current, superseded_by
FROM thoughts
WHERE id::text LIKE '5f81f4c7%'
   OR id::text LIKE 'f9ebb41e%'
   OR id::text LIKE 'edfcd7fe%';

COMMIT;
