#!/usr/bin/env bash
#
# Adapter coverage gate.
#
#   scripts/go-coverage.sh          run the suite and fail below the floor
#   MIN=80 scripts/go-coverage.sh   raise the floor for one run
#
# -coverpkg=./... is the point of this script. Without it `go test` credits a
# statement only to the package that declares the test, so a package with no
# _test.go file of its own reports 0.0% even when another package's tests drive
# every line of it. internal/engine/oe is the case that matters here: it has no
# test files, is exercised through internal/engine/mysql, and reads as 0% on a
# plain -coverprofile run when it is really covered. Attributing across the
# module gives a number worth gating on.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ADAPTER_DIR="${REPO_ROOT}/adapter"

# The floor tracks the measured total, a little below it so ordinary churn does
# not fail the build while a real regression does. Raise it when coverage rises.
MIN="${MIN:-78.0}"
PROFILE="${PROFILE:-${ADAPTER_DIR}/cover.out}"

cd "${ADAPTER_DIR}"

go test ./... -race -coverpkg=./... -coverprofile="${PROFILE}"

total="$(go tool cover -func="${PROFILE}" | awk '$1 == "total:" { gsub("%", "", $NF); print $NF }')"

if [[ -z "${total}" ]]; then
  echo "could not read a total from ${PROFILE}" >&2
  exit 1
fi

echo
echo "adapter coverage ${total}% (floor ${MIN}%)"

if awk -v got="${total}" -v min="${MIN}" 'BEGIN { exit !(got + 0 < min + 0) }'; then
  echo "coverage ${total}% is below the ${MIN}% floor" >&2
  echo "add tests, or lower MIN in scripts/go-coverage.sh with a reason" >&2
  exit 1
fi

# Per-package view, worst first, so a drop points at where to look.
echo
echo "lowest covered packages:"
# The regex is a string, not a /literal/, because BSD awk rejects a slash
# inside a bracket expression in a literal.
go tool cover -func="${PROFILE}" \
  | awk '$1 != "total:" {
           gsub("%", "", $NF)
           split($1, p, ":")
           n = p[1]
           sub("/[^/]*$", "", n)
           tot[n]++
           if ($NF + 0 > 0) cov[n]++
         }
         END { for (k in tot) printf "%6.1f%%  %s\n", 100 * cov[k] / tot[k], k }' \
  | sort -n | head -5
