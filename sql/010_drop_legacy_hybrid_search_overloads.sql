-- OpenBrain: OB-053 — Drop legacy hybrid_search() overloads
--
-- WHY THIS EXISTS
-- ----------------------------------------------------------------------------
-- hybrid_search() accumulated THREE coexisting overloads in the public schema
-- because successive migrations used CREATE OR REPLACE FUNCTION while CHANGING
-- the function's argument arity. CREATE OR REPLACE only replaces a function of
-- the *same identity* (same name + same ordered argument TYPES). Adding a
-- parameter changes the arity, which changes the identity, so PostgreSQL
-- ADDED a new function instead of replacing the old one:
--
--   005_hybrid_search.sql       → 6-arg  (text, vector, int, float8, float8, float8)
--   006_temporal_facts.sql      → 7-arg  (+ current_only boolean)       [ADDED]
--   007_search_filter_type.sql  → 8-arg  (+ filter_type text)           [ADDED]
--   008_untyped_embedding.sql   → 8-arg, vector(384)→vector             [REPLACED 007]
--
-- Note on 008: a vector typmod (the "384" in vector(384)) is NOT part of a
-- function's identity — vector(384) and vector are the same argument type to
-- PostgreSQL's overload resolver. So 008's CREATE OR REPLACE correctly
-- REPLACED the 007 8-arg in place; it did not add a fourth overload.
--
-- The 8-arg overload (as last replaced by 008, with the model-agnostic
-- untyped `vector` parameter) is the intended live signature. The 6-arg and
-- 7-arg overloads are dead code: nothing calls them, and because their
-- argument-type prefix is identical to the 8-arg, they make overload
-- resolution ambiguous when a caller's argument types are not fully pinned —
-- the source of the runtime error:
--
--   ERROR: function hybrid_search(...) is not unique
--
-- This migration drops the two dead overloads by their EXACT identity
-- arguments so only the 8-arg survives. The Go call site is separately pinned
-- to fully-typed arguments (see internal/db/search.go) so resolution is
-- unambiguous even if a stray overload is ever reintroduced.
--
-- IDEMPOTENT: DROP FUNCTION IF EXISTS with the exact signature is a no-op if
-- the overload is already gone. Safe to re-run via setup-db.sh in any order.
-- Crucially, the explicit argument lists below do NOT match the 8-arg
-- overload, so re-running this NEVER drops the live function.
-- ----------------------------------------------------------------------------

-- Drop the 6-arg overload (migration 005). Identity args, no defaults:
DROP FUNCTION IF EXISTS public.hybrid_search(
  text,
  vector,
  integer,
  double precision,
  double precision,
  double precision
);

-- Drop the 7-arg overload (migration 006: + current_only boolean):
DROP FUNCTION IF EXISTS public.hybrid_search(
  text,
  vector,
  integer,
  double precision,
  double precision,
  double precision,
  boolean
);
