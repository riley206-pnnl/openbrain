# OB-053 Runbook — Gated Runtime Steps (Craig approves; do NOT auto-run)

This card's code/SQL changes are committed to `fix/embedding-768-hybrid-search`.
The steps below touch the **live** OpenBrain database, the live `.env`, and the
running Go processes. They are deliberately NOT executed by the implementing
agent. Craig runs them after review.

## What the branch already fixes (no runtime action)

- **`sql/010_drop_legacy_hybrid_search_overloads.sql`** — drops the dead 6-arg
  and 7-arg `hybrid_search` overloads, leaving only the intended 8-arg.
  Idempotent (`DROP FUNCTION IF EXISTS <exact signature>`); never touches the
  8-arg.
- **`internal/db/search.go`** — the `hybrid_search` call now passes
  fully-typed args (`$2::vector(768)` plus explicit casts on every other arg),
  so resolution is unambiguous even if a stray overload reappears.
- **Config defaults** (`internal/config/config.go`) and **`.env.example`**
  already document `nomic-embed-text` / `768`. Verified, unchanged.

## Gated runtime steps (run in order, on the live host)

### 1. Back up the live DB first
```bash
pg_dump -Fc openbrain > openbrain_pre_ob053.dump
```

### 2. Apply the new migration to the live DB
Re-running `setup-db.sh` applies all `sql/*.sql` in filename order; 010 is the
only new file and is safe to re-apply (every prior migration is idempotent):
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

### 7. Decide: stop the legacy Python 384 server
If a legacy Python embedding/search server pinned to 384 is still running, it
is now a source of dimension drift. Decision for Craig: stop/decommission it so
the Go pipeline (768) is the only writer/reader. Not an automated step.

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
