#!/usr/bin/env bash
#
# Unit tests for scripts/lib/bounded.sh. No cluster: a fake kubectl that
# sleeps stands in for kubectl exec.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=bounded.sh
source "${REPO_ROOT}/scripts/lib/bounded.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
mkdir "${WORK}/bin"
# The fake execs sleep, as kubectl is one process, so a kill reaches it.
FAKE="fake-kubectl-$$"
printf '#!/bin/sh\necho started\nexec sleep "$1"\n' >"${WORK}/bin/${FAKE}"
chmod +x "${WORK}/bin/${FAKE}"
export PATH="${WORK}/bin:${PATH}"

# Counts live processes left from the fake or its sleeps.
lingering() { pgrep -f "${FAKE}|sleep 20|sleep 15" | grep -v "^$$\$" | wc -l | tr -d ' '; }

elapsed() { echo $((SECONDS - start)); }

# A command that finishes inside the bound returns its own status at once and
# leaves no watchdog behind holding the output open.
start=$SECONDS
rc=0
msg="$(bounded 20 "${FAKE}" 1 2>&1)" || rc=$?
[[ "${rc}" -eq 0 ]] || fail "quick command rc ${rc}: ${msg}"
[[ "$(elapsed)" -le 10 ]] || fail "quick command took $(elapsed)s under a 20s bound"
[[ "${msg}" == *started* ]] || fail "quick command output lost: ${msg}"
[[ "$(lingering)" -eq 0 ]] || fail "quick command left $(lingering) process(es) behind"
pass quick-command

# A status other than zero passes through unchanged.
rc=0
bounded 20 sh -c 'exit 7' || rc=$?
[[ "${rc}" -eq 7 ]] || fail "exit status 7 came back as ${rc}"
[[ "$(lingering)" -eq 0 ]] || fail "exit status case left $(lingering) process(es) behind"
pass exit-status

# A command that ends at once races the watchdog's start. Whatever the order,
# no process of the watchdog may outlive the call.
for _ in $(seq 1 100); do
  bounded 20 true
done
sleep 1
[[ "$(lingering)" -eq 0 ]] || fail "instant commands left $(lingering) watchdog process(es) behind"
pass instant-commands

# A hang is killed at the bound: nonzero status, the command dead, the capture
# returning without waiting for the command's own sleep.
start=$SECONDS
rc=0
msg="$(bounded 3 "${FAKE}" 15 2>&1)" || rc=$?
[[ "${rc}" -ne 0 ]] || fail "hung command returned 0: ${msg}"
[[ "$(elapsed)" -le 10 ]] || fail "hung command took $(elapsed)s under a 3s bound"
[[ "${msg}" != *Terminated* ]] || fail "kill message leaked: ${msg}"
sleep 1
[[ "$(lingering)" -eq 0 ]] || fail "hung command left $(lingering) process(es) behind"
pass hung-command

echo "all bounded lib tests passed"
