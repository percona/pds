#!/usr/bin/env bash
#
# Unit tests for scripts/check-pr.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="${REPO_ROOT}/scripts/check-pr.sh"
TEMPLATE="${REPO_ROOT}/.github/pull_request_template.md"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

[[ -x "${CHECK}" ]] || fail "scripts/check-pr.sh must exist and be executable"
[[ -f "${TEMPLATE}" ]] || fail ".github/pull_request_template.md must exist"

OUT="$(mktemp)"
trap 'rm -f "${OUT}"' EXIT

GOOD_TITLE="fix(adapter): keep unknown spec fields"
GOOD_BODY='## Summary

A scale PUT dropped fields the adapter does not model. It now sends them back.

## What changed

- The engine keeps the raw spec and patches only the fields it owns.

## Verification

1. Create a Dev database, add an unmodeled field with kubectl, scale it.
2. Expect the field to survive the scale.

Ran on kind on 2026-10-01: the field survived. `go test ./... -race` passed.

## Not verified

Nothing.'

run() {
  PR_TITLE="$1" PR_BODY="$2" "${CHECK}" >"${OUT}" 2>&1
}

expect_ok() {
  local label="$1"
  if run "$2" "$3"; then
    pass "${label}"
  else
    cat "${OUT}" >&2
    fail "${label}: expected a pass"
  fi
}

expect_fail() {
  local label="$1" needle="$4"
  if run "$2" "$3"; then
    fail "${label}: expected a failure"
  fi
  grep -q -- "${needle}" "${OUT}" || {
    cat "${OUT}" >&2
    fail "${label}: the report does not say ${needle}"
  }
  pass "${label}"
}

expect_ok "a conventional title and a full body pass" "${GOOD_TITLE}" "${GOOD_BODY}"
expect_ok "the mcp scope passes" "refactor(mcp): serve a hub only" "${GOOD_BODY}"
expect_ok "the plugins scope passes" "feat(plugins): ship the pds plugin" "${GOOD_BODY}"
expect_ok "a title without a scope passes" "docs: say where the lab keeps its logs" "${GOOD_BODY}"
expect_ok "a breaking change marker passes" "feat(chart)!: drop the legacy release name" "${GOOD_BODY}"
expect_ok "an optional extra section passes" "${GOOD_TITLE}" "${GOOD_BODY}

## Notes

Follow-up left out on purpose."

expect_ok "a GitHub revert title passes" 'Revert "fix(adapter): keep unknown spec fields"' "${GOOD_BODY}"
expect_ok "a GitHub issue number passes" "${GOOD_TITLE}" "${GOOD_BODY}

Fixes #123."
expect_ok "a Jira key passes" "${GOOD_TITLE}" "${GOOD_BODY}

Fixes BC-123."
expect_ok "a tracker link inside a comment is ignored" "${GOOD_TITLE}" "${GOOD_BODY}

<!-- see https://example.atlassian.net/browse/BC-123 -->"

expect_fail "a free form title fails" "Keep unknown spec fields" "${GOOD_BODY}" "not a Conventional Commit"
expect_fail "an unknown type fails" "feature(adapter): keep unknown spec fields" "${GOOD_BODY}" "type 'feature'"
expect_fail "an unknown scope fails" "fix(api): keep unknown spec fields" "${GOOD_BODY}" "scope 'api'"
expect_fail "an upper case summary fails" "fix(adapter): Keep unknown spec fields" "${GOOD_BODY}" "upper case"
expect_fail "a trailing period fails" "fix(adapter): keep unknown spec fields." "${GOOD_BODY}" "period"
expect_fail "a long title fails" "fix(adapter): $(printf 'a%.0s' {1..100})" "${GOOD_BODY}" "characters"

expect_fail "an empty body fails" "${GOOD_TITLE}" "" "no '## Summary' section"
expect_fail "a missing section fails" "${GOOD_TITLE}" "${GOOD_BODY//## Not verified/## Gaps}" "no '## Not verified' section"
expect_fail "the untouched template fails" "${GOOD_TITLE}" "$(cat "${TEMPLATE}")" "'## Summary' section is empty"
expect_fail "a linked issue fails" "${GOOD_TITLE}" "${GOOD_BODY}

See https://example.atlassian.net/browse/BC-123." "#123"

echo "check-pr tests passed"
