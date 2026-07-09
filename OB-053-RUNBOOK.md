# OB-053 Runbook — Gated Runtime Steps (Craig approves; do NOT auto-run)

This card's code/SQL changes are committed to `fix/embedding-768-hybrid-search`.
The steps below touch the **live** OpenBrain database, the live `.env`, and the
running Go processes. They are deliberately NOT executed by the implementing
agent. Craig runs them after review.

## What the branch already fixes (no runtime action)

- **`sql/010_drop_legacy_hybrid_search_overloads.sql`** — drops the legacy 6-arg
  and 7-arg `hybrid_search` overloads, leaving only the intended 8-arg.
  Idempotent (`DROP FUNCTION IF EXISTS <exact signature>`); never touches the
  8-arg.
- **`internal/db/search.go`** — the `hybrid_search` call now passes
  fully-typed args (`$2::vector(<configured dim>)` plus explicit casts on every
  other arg), so resolution is unambiguous even if a stray overload reappears.
  The embedding cast dimension follows `OPENBRAIN_EMBEDDING_DIM` (default 768),
  not a hardcoded literal.
- **Config defaults** (`internal/config/config.go`) and **`.env.example`**
  already document `nomic-embed-text` / `768`. Verified, unchanged.

### Important: the legacy 7-arg overload IS still called

The earlier draft of this runbook (and the original 010 comment) claimed the
6-/7-arg overloads were "dead code: nothing calls them." **That is false.** The
still-running legacy Python server calls the **7-arg** `hybrid_search` directly
(`src/openbrain/db.py`, ~L146-169: `SELECT * FROM hybrid_search($1, $2::vector,
$3, $4, $5, $6, $7)` — 7 positional args, **bare** `$2::vector`). Two facts
mitigate the risk of dropping the 7-arg out from under that caller:

1. **It rebinds, not breaks.** The surviving 8-arg overload's 8th parameter
   (`filter_type TEXT DEFAULT NULL`) is defaultable, so a 7-positional call
   resolves to the 8-arg with `filter_type => NULL`. After 010 drops the 7-arg,
   the Python call still finds a match.
2. **Failure would be LOUD, not silent.** If it somehow did not rebind, asyncpg
   raises a hard `UndefinedFunctionError` (Wren confirmed every Python search
   path surfaces the exception rather than returning an empty result set). There
   is no silent-empty failure mode to mask a regression.

### Deploy ordering is unconstrained (Pike)

The Go cast fix is **self-sufficient**: the fully-typed `$2::vector(768)` call
makes pgx send a typed Parse, which resolves the 8-arg overload unambiguously
**with or without** migration 010. Consequence: the Go binary deploy and the 010
migration can land in **either order** — there is no correctness gate coupling
them. (010 is still worth applying to remove the ambiguity at its source for any
not-fully-typed caller, but it does not gate the Go fix.)

## Precondition (decide BEFORE applying migration 010)

### P1. Retire — or confirm tolerance of — the legacy Python 384 server

A legacy Python embedding/search server pinned to 384 is a source of dimension
drift, and (per "Important" above) it is the live caller of the 7-arg overload
that 010 drops. Before applying 010, **decide one of**:

- **Retire it** — stop/decommission the Python server so the Go pipeline (768)
  is the only writer/reader. Preferred end state. After this, dropping the 7-arg
  is unambiguously safe.
- **Confirm tolerance** — if it must keep running for now, confirm the rebind in
  fact 1 above holds on the live DB (the 8-arg's defaultable `filter_type` covers
  the 7-positional call) and accept that the Python path will exercise the 8-arg.
  Its bare `$2::vector` cast does not reintroduce ambiguity once 010 has removed
  the competing 6-/7-arg overloads.

This decision is a precondition, not an afterthought: it gates whether 010 is
safe to apply against the live function set.

## Gated runtime steps (run in order, on the live host)

### 1. Back up the live DB first
```bash
pg_dump -Fc openbrain > openbrain_pre_ob053.dump
```

### 2. Apply the new migration to the live DB
**Precondition P1 (above) must be decided first** — 010 drops the 7-arg overload
that the legacy Python server still calls. Once P1 is settled, re-running
`setup-db.sh` applies all `sql/*.sql` in filename order; 010 is the only new file
and is safe to re-apply (every prior migration is idempotent). Note (Pike): this
step is NOT gated on the Go binary deploy — the Go cast fix is self-sufficient,
so 010 and the Go deploy can land in either order:
```bash
OPENBRAIN_DB_PASSWORD=<live-password> ./scripts/setup-db.sh
```
Or apply 010 alone:
```bash
sudo -u postgres psql -d openbrain -f sql/010_drop_legacy_hybrid_search_overloads.sql
```
Verify only the 8-arg overload remains:
```bash
sudo -u postgres psql -d openbrain -c \
  "SELECT pg_get_function_identity_arguments(oid)
   FROM pg_proc WHERE proname = 'hybrid_search';"
# Expect exactly ONE row, the 8-arg:
#   text, vector, integer, double precision, double precision,
#   double precision, boolean, text
```

### 3. Correct the live `.env` to nomic / 768
The live `.env` carried `all-minilm` / `384` (the config-drift root cause).
Edit the real `.env` (NOT tracked; not in this worktree):
```
OPENBRAIN_EMBEDDING_MODEL=nomic-embed-text
OPENBRAIN_EMBEDDING_DIM=768
```
Confirm Ollama has the model: `ollama pull nomic-embed-text`.

### 4. Reconcile `embedding_config` if needed
`ValidateEmbeddingConfig` (startup) compares env vs. the `embedding_config`
row. Migration 009 seeds `nomic-embed-text` / `768` only on first insert
(`ON CONFLICT DO NOTHING`). If a prior run wrote `all-minilm` / `384`, align it:
```bash
openbrain reembed         # re-embeds all thoughts AND updates embedding_config
# (only needed if any embeddings are stale or the row disagrees with env)
```
All 289 thoughts are already 768-dim per the OB-053 diagnosis, so a re-embed is
likely unnecessary — but the `embedding_config` row must read `768` or startup
validation will refuse to run.

### 5. embedding_config GRANT gap (verify, fix if present)
The diagnosis hit `permission denied` reading `embedding_config` as role
`openbrain`. Root cause: `setup-db.sh` grants `ON ALL TABLES` *once*, in a step
that ran before 009 created the table on the live host, and
`ALTER DEFAULT PRIVILEGES` only covers tables created afterward by the granting
role. Check and fix:
```bash
sudo -u postgres psql -d openbrain -c \
  "SELECT has_table_privilege('openbrain','embedding_config','SELECT');"
# If 'f', grant explicitly:
sudo -u postgres psql -d openbrain -c \
  "GRANT SELECT, UPDATE ON embedding_config TO openbrain;"
```
(A fresh `setup-db.sh` run also re-runs the blanket GRANT step after migrations,
which covers it — but the explicit grant is the surgical fix on the live DB.)

### 6. Restart the Go processes
```bash
systemctl --user restart openbrain-watchd openbrain-web
# (or the unit names in use on the host)
```
Then smoke-test a hybrid search and confirm no
`function hybrid_search(...) is not unique` error and semantic results return.

### 7. Follow through on precondition P1
If P1 was settled as "confirm tolerance" rather than "retire," revisit the
decision now that 768 is live: stop/decommission the legacy Python 384 server so
the Go pipeline (768) is the only writer/reader and dimension drift is
eliminated. Not an automated step. (If P1 was already "retire," this is done.)

## Rollback

- **Migration 010**: dropping the dead overloads is safe and reversible by
  re-running migrations 005 + 006 (which recreate the 6-/7-arg overloads), but
  there is no operational reason to — they were never called. The 8-arg live
  function is untouched, so search keeps working with 010 applied.
- **Code (`search.go`)**: revert the commit; the bare-cast version returns the
  ambiguity bug, so don't.
- **`.env` / `embedding_config`**: restore from `openbrain_pre_ob053.dump`
  (step 1) if the re-embed or config edit goes wrong.
- The data layer (289 × 768-dim thoughts, bare-`vector` column, HNSW index) is
  not modified by this card; nothing to roll back there.
