#!/usr/bin/env bash
#
# Checks a lab that scripts/up.sh has already started.
#
#   scripts/lab-platform.sh
#
# The hub must list the connected environment as connected, the agent
# Deployment on that cluster must be Ready, and the Flux Kustomizations
# instances and platform must be Ready. Connect creates platform only on
# amd64, which is the runner this check is for. Nothing here creates a
# database.
#
# Environment, matching the lab:
#   SPOKE_CLUSTER, UI_PORT, HUB_NS
#   BOOTSTRAP_ORGANIZATION, BOOTSTRAP_PASSWORD
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/lab.sh
source "${REPO_ROOT}/scripts/lib/lab.sh"

_LAB_PLATFORM_TMP=""

# Names Flux must have Ready. platform is not optional here: an arm64 lab
# never creates it, and this check is the amd64 runner.
lab_platform_kustomizations() {
  printf '%s\n' instances platform
}

# Empty, and status 0, when name is connected. Otherwise a sentence and
# status 1. Parsing is the environments list; a neighbour does not count.
lab_platform_environment_error() {
  local json="$1"
  local name="$2"
  local status
  status="$(lab_environment_status "${json}" "${name}")"
  if [[ "${status}" == "connected" ]]; then
    return 0
  fi
  if [[ -z "${status}" ]]; then
    printf '%s\n' "GET /api/environments does not list ${name}."
    return 1
  fi
  printf '%s\n' "GET /api/environments lists ${name} as ${status}, want connected."
  return 1
}

lab_platform_cleanup() {
  if [[ -n "${_LAB_PLATFORM_TMP}" ]]; then
    rm -rf "${_LAB_PLATFORM_TMP}"
    _LAB_PLATFORM_TMP=""
  fi
}

lab_platform_run() {
  local spoke port organization password ns context jar json err name
  spoke="${SPOKE_CLUSTER:-spoke-a}"
  port="${UI_PORT:-18080}"
  organization="${BOOTSTRAP_ORGANIZATION:-dev}"
  password="${BOOTSTRAP_PASSWORD:-percona-dev-login}"
  ns="${HUB_NS:-everest-system}"
  context="kind-${spoke}"

  _LAB_PLATFORM_TMP="$(mktemp -d)"
  trap lab_platform_cleanup EXIT
  jar="${_LAB_PLATFORM_TMP}/cookies"

  printf '\n== sign in\n'
  lab_wait_login "${port}" "${organization}" "${password}" "${jar}"

  printf '\n== environments\n'
  json="$(lab_fetch_environments "${port}" "${jar}")"
  if ! err="$(lab_platform_environment_error "${json}" "${spoke}")"; then
    printf '%s\n' "${err}" >&2
    return 1
  fi
  echo "environment ${spoke} is connected"

  printf '\n== agent\n'
  kubectl --context "${context}" -n "${ns}" rollout status deploy/pds-adapter --timeout=180s

  printf '\n== flux\n'
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    if ! lab_kustomization_ready "${context}" "${name}"; then
      echo "Flux Kustomization ${name} is not Ready on ${context}." >&2
      return 1
    fi
    echo "Flux Kustomization ${name} is Ready"
  done < <(lab_platform_kustomizations)

  echo "lab platform checks passed"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  if [[ $# -gt 0 ]]; then
    echo "usage: $(basename "$0")" >&2
    exit 2
  fi
  lab_platform_run
fi
