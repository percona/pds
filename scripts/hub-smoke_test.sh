#!/usr/bin/env bash
#
# Flag checks for scripts/hub-smoke.sh. No lab is started: every case here is
# refused before the script touches docker or kubectl.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/hub-smoke.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

# The usage text goes to stderr and the exit status is 2.
run_refused() {
  RC=0
  ERR="$("${SCRIPT}" "$@" 2>&1 >/dev/null)" || RC=$?
}

run_refused --reuse-lab
[[ "${RC}" -eq 2 ]] || fail "--reuse-lab: rc=${RC}, want 2"
pass "--reuse-lab is refused"

run_refused --no-such-flag
[[ "${RC}" -eq 2 ]] || fail "unknown flag: rc=${RC}, want 2"
[[ "${ERR}" == *"--lab-only"* ]] || fail "usage does not name --lab-only: ${ERR}"
[[ "${ERR}" != *"--reuse-lab"* ]] || fail "usage still names --reuse-lab: ${ERR}"
pass "usage names --lab-only and not --reuse-lab"

run_refused --engine oracle
[[ "${RC}" -eq 2 ]] || fail "unknown engine: rc=${RC}, want 2"
pass "an unknown engine is refused"

echo "hub-smoke flag checks passed"
