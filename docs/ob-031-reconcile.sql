-- OB-031 one-off data-debt reconciliation.
--
-- MANUAL STEP. This statement is NOT run by the application and is NOT a
-- migration. An operator runs it once, by hand, against the live openbrain
-- database after the OB-031 atomic-supersede fix is deployed. It retires the
-- two stale thoughts that the pre-fix silent-failure path left live, leaving a
-- single live thought for the Sadie voice-canon slot.
--
-- Canon slot after reconciliation:
--   5f81f4c7-5640-420e-ba66-9cb205ac06a0  canonical Sadie voice canon   KEEP (stays live)
--   f9ebb41e-a045-4fc7-aeff-956f5392456c  stale                          retired
--   edfcd7fe-598b-4b3b-9dec-96105f2825f8  orphan pointer                 retired
--
-- Full UUIDs, not 8-char prefixes: a prefix match risks a collision that
-- retires the wrong row or resurrects an already-retired one. Every match
-- below is full-UUID equality, and the retire UPDATE is additionally guarded
-- with AND is_current = TRUE so a re-run cannot touch an already-retired or
-- unintended row.
--
-- Run inside a transaction so you can inspect before COMMIT.
--
-- This statement is idempotent: re-running it leaves the same end state.

BEGIN;

UPDATE thoughts
SET is_current    = FALSE,
    valid_until   = COALESCE(valid_until, NOW()),
    superseded_by = '5f81f4c7-5640-420e-ba66-9cb205ac06a0'::uuid
WHERE id IN (
    'f9ebb41e-a045-4fc7-aeff-956f5392456c'::uuid,
    'edfcd7fe-598b-4b3b-9dec-96105f2825f8'::uuid
  )
  AND is_current = TRUE;

-- Defensively re-assert that the KEEP thought is live and unretired, in case a
-- prior half-write touched it. Guarded the same way: only ever touches the
-- one exact KEEP row.
UPDATE thoughts
SET is_current    = TRUE,
    valid_until   = NULL,
    superseded_by = NULL
WHERE id = '5f81f4c7-5640-420e-ba66-9cb205ac06a0'::uuid;

-- Expect: exactly one live thought across the three ids (the KEEP row).
-- Inspect, then COMMIT (or ROLLBACK if the count is not 1).
SELECT id, is_current, superseded_by
FROM thoughts
WHERE id IN (
  '5f81f4c7-5640-420e-ba66-9cb205ac06a0'::uuid,
  'f9ebb41e-a045-4fc7-aeff-956f5392456c'::uuid,
  'edfcd7fe-598b-4b3b-9dec-96105f2825f8'::uuid
);

COMMIT;
