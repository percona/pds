#!/usr/bin/env bash
#
# Pure checks for scripts/lab-platform.sh. No cluster is started.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lab-platform.sh
source "${REPO_ROOT}/scripts/lab-platform.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

# Captures stdout. A non-zero status is the case under test, so it must not
# abort the script.
run_error() {
  local json="$1"
  local name="$2"
  RC=0
  ERR="$(lab_platform_environment_error "${json}" "${name}")" || RC=$?
}

connected='{"hub":true,"environments":[{"name":"spoke-a","status":"connected","namespace":"pds"}]}'
run_error "${connected}" spoke-a
[[ "${RC}" -eq 0 && -z "${ERR}" ]] || fail "connected environment: rc=${RC} err=${ERR}"
pass "connected environment"

# A neighbour that is connected does not satisfy the environment under test.
other='{"hub":true,"environments":[{"name":"spoke-b","status":"connected"},{"name":"spoke-a","status":"unreachable"}]}'
run_error "${other}" spoke-a
[[ "${RC}" -ne 0 ]] || fail "unreachable should fail"
[[ "${ERR}" == *"spoke-a"* && "${ERR}" == *"unreachable"* ]] || fail "unreachable message: ${ERR}"
pass "unreachable environment"

missing='{"hub":true,"environments":[{"name":"spoke-b","status":"connected"}]}'
run_error "${missing}" spoke-a
[[ "${RC}" -ne 0 ]] || fail "a missing environment should fail"
[[ "${ERR}" == *"spoke-a"* && "${ERR}" == *"does not list"* ]] || fail "missing message: ${ERR}"
pass "missing environment"

run_error '{"hub":true,"environments":[]}' spoke-a
[[ "${RC}" -ne 0 && "${ERR}" == *"does not list"* ]] || fail "empty list: rc=${RC} err=${ERR}"
pass "empty environment list"

run_error '' spoke-a
[[ "${RC}" -ne 0 && "${ERR}" == *"does not list"* ]] || fail "empty body: rc=${RC} err=${ERR}"
pass "empty body"

run_error 'not-json' spoke-a
[[ "${RC}" -ne 0 && "${ERR}" == *"does not list"* ]] || fail "bad json: rc=${RC} err=${ERR}"
pass "bad json"

# Connect omits platform on arm64. This check is the amd64 runner, so both
# names are required.
names="$(lab_platform_kustomizations)"
[[ "$(printf '%s\n' "${names}" | grep -c .)" -eq 2 ]] || fail "kustomization count: ${names}"
printf '%s\n' "${names}" | grep -qx instances || fail "instances missing: ${names}"
printf '%s\n' "${names}" | grep -qx platform || fail "platform missing: ${names}"
pass "instances and platform"

echo "lab-platform tests passed"
