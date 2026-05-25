#!/usr/bin/env bash
#
# govulncheck-gate.sh (KM-178)
#
# Runs `govulncheck -json ./...`, then fails ONLY on reachable findings whose
# OSV id is not present in the allowlist. govulncheck has no native ignore
# mechanism, so this wrapper provides the documented suppression path:
# .github/govulncheck-allowlist.txt.
#
# Design:
#   - govulncheck emits a stream of concatenated JSON objects (NOT line-
#     delimited). `jq` reads concatenated JSON natively, so we pipe straight
#     into it. A "finding" object is only emitted when a vulnerability is
#     reachable from this module's code; the 100+ "osv" records are just the
#     scanned DB advisories and are NOT findings.
#   - We collect the distinct `.finding.osv` ids, subtract the allowlist, and
#     fail (exit 1) if any remain. A clean tree yields zero findings → pass.
#   - We do NOT trust govulncheck's own exit code alone, because the whole
#     point is to let the allowlist suppress specific ids. We compute the
#     blocking set ourselves.
#
# Requires: govulncheck and jq on PATH. GOTOOLCHAIN is set by the caller
# (the workflow pins it to the Dockerfile builder version).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ALLOWLIST="${REPO_ROOT}/.github/govulncheck-allowlist.txt"

echo "::group::govulncheck (Go $(go env GOVERSION 2>/dev/null || go version))"
echo "Allowlist: ${ALLOWLIST}"

# Capture JSON + stderr to temp files so we can both human-print and machine-
# parse them without re-running the (slow) scan. Both are mktemp'd and removed
# on EXIT so a persistent self-hosted runner never accumulates working-tree
# litter (KM-178 review LOW).
JSON_OUT="$(mktemp)"
ERR_OUT="$(mktemp)"
trap 'rm -f "${JSON_OUT}" "${ERR_OUT}"' EXIT

# Run govulncheck and CAPTURE ITS EXIT CODE — never `|| true`, which would
# discard a tool crash and let the gate fall through to a green pass. The exit
# code is the primary signal of tool health:
#   0 = ran cleanly, no vulns
#   3 = ran cleanly, vulns found (we still parse + allowlist below)
#   anything else (1, 2, ...) = govulncheck itself FAILED (bad flags, build
#       error, DB fetch failure, crash). That is NOT a clean tree — fail closed.
set +e
govulncheck -json ./... > "${JSON_OUT}" 2>"${ERR_OUT}"
GVC_RC=$?
set -e

if [[ -s "${ERR_OUT}" ]]; then
  echo "govulncheck stderr:"
  cat "${ERR_OUT}"
fi

# Tool failure (exit code not in {0,3}) → fail closed. We do this BEFORE looking
# at the JSON: a crashed run may have written partial/garbage output that would
# otherwise parse to "zero findings" and masquerade as clean.
if [[ "${GVC_RC}" -ne 0 && "${GVC_RC}" -ne 3 ]]; then
  echo "::error::govulncheck exited ${GVC_RC} (expected 0=clean or 3=vulns). This is a TOOL FAILURE, not a clean pass — failing closed." >&2
  echo "------ govulncheck stderr ------" >&2
  cat "${ERR_OUT}" >&2
  echo "--------------------------------" >&2
  exit 2
fi

# JSON-validity guard. Replaces the old `[[ ! -s ]]` (empty-file) check, which
# only caught a *fully empty* file and let truncated / whitespace-only / partial
# (mid-stream crash) output through to a green pass. `jq -e .` validates that the
# stream is well-formed JSON; truncated or garbage output fails here and we fail
# closed rather than silently extracting zero findings.
if ! jq -e . "${JSON_OUT}" >/dev/null 2>&1; then
  echo "::error::govulncheck output is not valid JSON (truncated stream, whitespace-only, or mid-run crash) — failing closed. NOT a clean pass." >&2
  echo "------ captured output (first 40 lines) ------" >&2
  head -40 "${JSON_OUT}" >&2
  echo "----------------------------------------------" >&2
  exit 2
fi

# All reachable finding OSV ids (deduped, sorted). A finding object always
# carries a `.finding.osv` id when a vuln is reachable.
#
# The extraction is split into two explicit stages so a jq parse error is NOT
# conflated with the benign "no lines matched" case:
#
#   Stage 1 (jq): run jq ALONE — not buried in a pipeline — and capture its
#     exit code directly. `set -e` does NOT catch a failure of a non-final
#     pipeline element, so a `jq ... | sort` would mask a jq crash behind
#     sort's exit 0. By running jq on its own we can assert GVC_JQ_RC == 0 and
#     fail closed otherwise. The JSON-validity guard above already proved the
#     stream parses, so a non-zero here is a real filter/extraction fault.
#   Stage 2 (grep): `grep -v '^$'` on the captured jq output only normalizes
#     away blank lines. Its exit 1 means "every line was blank" (legitimately
#     zero findings → clean), which we tolerate with `|| true`. Because jq has
#     already run and been checked, this `|| true` can never hide a jq crash.
set +e
FINDINGS_RAW="$(jq -r 'select(.finding != null) | .finding.osv' "${JSON_OUT}")"
GVC_JQ_RC=$?
set -e
if [[ "${GVC_JQ_RC}" -ne 0 ]]; then
  echo "::error::jq failed (exit ${GVC_JQ_RC}) while extracting findings from valid JSON — failing closed rather than reporting zero findings." >&2
  exit 2
fi
ALL_FINDINGS="$(printf '%s\n' "${FINDINGS_RAW}" | sort -u | grep -v '^$' || true)"

# Build the allowlist set (strip comments + blanks).
ALLOWED="$(grep -vE '^\s*(#|$)' "${ALLOWLIST}" 2>/dev/null | tr -d ' \t' | sort -u || true)"

echo "::endgroup::"

if [[ -z "${ALL_FINDINGS}" ]]; then
  echo "✅ govulncheck: No vulnerabilities found. Gate PASSES."
  exit 0
fi

echo "govulncheck reported the following reachable findings:"
echo "${ALL_FINDINGS}" | sed 's/^/  - /'
echo ""

# Blocking = findings - allowlist.
BLOCKING="$(comm -23 <(echo "${ALL_FINDINGS}") <(echo "${ALLOWED}") | grep -v '^$' || true)"

if [[ -n "${ALLOWED}" ]]; then
  echo "Allowlisted (suppressed) ids from ${ALLOWLIST}:"
  echo "${ALLOWED}" | sed 's/^/  - /'
  echo ""
fi

if [[ -z "${BLOCKING}" ]]; then
  echo "✅ govulncheck: all reachable findings are allowlisted. Gate PASSES."
  echo "   (Allowlist entries are temporary — each should be tracked to a fix card.)"
  exit 0
fi

echo "❌ govulncheck: the following findings are NOT allowlisted and BLOCK the merge:"
echo "${BLOCKING}" | sed 's/^/  - /'
echo ""
echo "Fix options:"
echo "  1. (Preferred) Bump the dependency / Go toolchain that clears the finding."
echo "  2. (Relief valve) Add the id to ${ALLOWLIST} with a justification + tracking card."
echo ""
echo "Full human-readable report:"
echo "::group::govulncheck details"
govulncheck ./... || true
echo "::endgroup::"

exit 1
