#!/usr/bin/env bash
#
# Turns the smoke workflow's `suites` input, a comma separated list of mysql,
# postgresql, mcp and flux, into the JSON array its matrix runs. An unknown name is
# refused rather than dropped, so a typo cannot quietly skip a suite.
#
#   scripts/smoke-suites.sh "mysql, mcp"       ["mysql","mcp"]
set -euo pipefail

known=(mysql postgresql mcp flux)
requested="$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]' | tr -d ' ')"

IFS=',' read -r -a tokens <<< "${requested}"
for token in ${tokens[@]+"${tokens[@]}"}; do
  [[ -z "${token}" ]] && continue
  found=false
  for suite in "${known[@]}"; do
    [[ "${token}" == "${suite}" ]] && found=true
  done
  if [[ "${found}" != true ]]; then
    echo "unknown suite '${token}': use ${known[*]}" >&2
    exit 2
  fi
done

chosen=()
for suite in "${known[@]}"; do
  if [[ ",${requested}," == *",${suite},"* ]]; then
    chosen+=("\"${suite}\"")
  fi
done
if [[ ${#chosen[@]} -eq 0 ]]; then
  echo "no suite requested: use ${known[*]}" >&2
  exit 2
fi
(IFS=,; echo "[${chosen[*]}]")
