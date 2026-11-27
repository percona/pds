#!/usr/bin/env bash
#
# Checks for scripts/smoke-suites.sh. Nothing is started.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/smoke-suites.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

expect_list() {
  local name="$1" input="$2" want="$3" got
  got="$("${SCRIPT}" "${input}")" || fail "${name}: refused '${input}'"
  [[ "${got}" == "${want}" ]] || fail "${name}: got ${got}, want ${want}"
  pass "${name}"
}

expect_refused() {
  local name="$1" input="$2" rc=0
  "${SCRIPT}" "${input}" >/dev/null 2>&1 || rc=$?
  [[ "${rc}" -eq 2 ]] || fail "${name}: rc=${rc}, want 2"
  pass "${name}"
}

expect_list all "mysql,postgresql,mcp,flux" '["mysql","postgresql","mcp","flux"]'
expect_list flux "flux" '["flux"]'
expect_list one "mcp" '["mcp"]'
expect_list spaces-and-case " MySQL , mcp " '["mysql","mcp"]'
expect_list order-is-fixed "flux,mcp,mysql" '["mysql","mcp","flux"]'
expect_list duplicate "mcp,mcp" '["mcp"]'
expect_refused typo "mysql,postgres"
expect_refused unknown "oracle"
expect_refused empty ""
expect_refused only-commas ",,"

echo "smoke-suites checks passed"
