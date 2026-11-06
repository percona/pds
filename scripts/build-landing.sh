#!/usr/bin/env bash
#
# Builds the public landing page into OUTDIR, where the UI image picks it up.
# Only the hosted deployment serves it; the chart and the bring-up scripts
# build the console alone.
#
#   scripts/build-landing.sh apps/ui/dist/landing
#
# VITE_CONSOLE_URL is read from the environment at build time. Unset, Sign in
# points at /app/ on the same host.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $(basename "$0") OUTDIR" >&2
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$1"
[[ "${out}" = /* ]] || out="${PWD}/${out}"

# The landing page is its own npm project, outside the workspace.
npm --prefix "${REPO_ROOT}/landing" ci --no-audit --no-fund
npm --prefix "${REPO_ROOT}/landing" run build -- --outDir "${out}" --emptyOutDir
