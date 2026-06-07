# openbrain CI workflows

| Workflow | Required context | Purpose |
|---|---|---|
| `test.yml` | `Unit tests` | Go build / vet / `go test -race` |
| `cve-gate.yml` | `cve-gate` | govulncheck (blocking) + gosec (advisory), aggregated |
| `reviews-complete.yml` | `reviews-complete` | Label gate: all four `reviewed:*` labels present |

## Runner policy — PUBLIC-REPO CARVE-OUT

All three workflows run on **`runs-on: ubuntu-latest` (GitHub-hosted runners)**, NOT
the private self-hosted fleet.

This is a **deliberate, documented exception** to the org-wide "self-hosted only"
DevOps rule, **scoped to PUBLIC repos**:

- openbrain is a **public** repo. The private self-hosted fleet **denies public
  repos** — running public-repo CI (including fork PRs) on private infrastructure is
  a code-execution exposure.
- GitHub-hosted `ubuntu-latest` runners are **free for public repos** and **sandboxed
  per job** (fresh ephemeral VM each run), so they are the correct fit here.
- **Private repos still use the self-hosted fleet** per the standard rule. This
  carve-out does not generalize.

### Consequences of the carve-out

- **`test.yml`** runs the `Unit tests` job **directly on the host** (no container).
  ubuntu-latest ships gcc / build-essential, so cgo is on by default and
  `go test -race` works natively. The Go toolchain is pinned via
  `actions/setup-go@v6` to **`1.26.4`** — matching go.mod's `go 1.26.4` line.
- **`cve-gate.yml`** pins Go to the same `1.26.4` via `setup-go`. The ephemeral
  pinned + checksum-verified `jq` provisioning step is **kept** even though
  ubuntu-latest ships jq — it pins an audited jq and keeps the gate copy-pasteable
  with openknowledge's.
- **`reviews-complete.yml`** is tool-free (pure GitHub expressions + shell builtins),
  so the runner switch is a no-op for it — it runs anywhere.

Per-workflow rationale lives in each file's header comment.
