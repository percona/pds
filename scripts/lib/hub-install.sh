#!/usr/bin/env bash
#
# Helm arguments for installing this checkout as a hub on a cluster that
# already exists. Sourced by scripts/up.sh. The caller has already filled
# HELM_SETS with image and login settings.
#
# A hub is the UI and the adapter. It does not install the database platform,
# monitoring, or a backup bucket. Desired state lives in a PersistentVolume
# unless a GitHub org is set, in which case Connect creates one repo per environment.
#
# shellcheck shell=bash

# Reject a value that would split a helm --set-string argument.
hub_install_one_token() {
  local label="$1"
  local value="$2"
  local quote="'"
  if [[ "${value}" == *$'\n'* || "${value}" == *$'\r'* || "${value}" == *[[:space:]]* || "${value}" == *'"'* || "${value}" == *"${quote}"* ]]; then
    echo "${label} must be a single token" >&2
    return 1
  fi
}

# Derive HUB_PUBLIC_URL and HUB_GITHUB_SECRET, and reject combinations the
# chart will not install. Safe to call before any cluster change.
hub_install_resolve() {
  local public="${HUB_PUBLIC_URL:-}"

  if [[ -z "${public}" && -n "${INGRESS_HOST:-}" ]]; then
    if [[ -n "${INGRESS_TLS_SECRET:-}" ]]; then
      public="https://${INGRESS_HOST}"
    else
      public="http://${INGRESS_HOST}"
    fi
    HUB_PUBLIC_URL="${public}"
  fi
  if [[ -z "${public}" ]]; then
    echo "hub mode needs a public URL spokes can reach. Pass --hub-public-url or --ingress-host." >&2
    return 1
  fi
  hub_install_one_token "hub public URL" "${public}" || return 1

  if [[ -n "${HUB_GITHUB_SECRET:-}" && -z "${HUB_GITHUB_ORG:-}" ]]; then
    echo "--hub-github-secret needs --hub-github-org" >&2
    return 1
  fi
  if [[ -n "${HUB_GITHUB_ORG:-}" && -n "${HUB_GIT_URL:-}" ]]; then
    echo "pass either --hub-github-org or --hub-git-url" >&2
    return 1
  fi
  if [[ -n "${HUB_GITHUB_ORG:-}" ]]; then
    hub_install_one_token "GitHub org" "${HUB_GITHUB_ORG}" || return 1
    HUB_GITHUB_SECRET="${HUB_GITHUB_SECRET:-hub-github}"
    hub_install_one_token "GitHub secret" "${HUB_GITHUB_SECRET}" || return 1
  fi
  if [[ -n "${HUB_GIT_URL:-}" ]]; then
    hub_install_one_token "hub git URL" "${HUB_GIT_URL}" || return 1
  fi
  if [[ -n "${HUB_CLUSTER_REGION:-}" ]]; then
    hub_install_one_token "hub region" "${HUB_CLUSTER_REGION}" || return 1
  fi
}

# Append hub settings to HELM_SETS. The chart defaults are already a hub.
# Reads HUB_PUBLIC_URL, HUB_GITHUB_ORG, HUB_GITHUB_SECRET, HUB_GIT_URL,
# HUB_CLUSTER_REGION, INGRESS_HOST, and INGRESS_TLS_SECRET.
hub_install_prepare() {
  : "${CHART_DIR:?CHART_DIR is required}"
  hub_install_resolve || return 1

  # shellcheck disable=SC2034 # helm-install.sh expands this after sourcing; empty because the chart defaults are the hub
  HELM_VALUE_FILES=()
  HELM_SETS+=(
    --set stack.enabled=false
    --set-string "adapter.hub.publicURL=${HUB_PUBLIC_URL}"
  )
  if [[ -n "${HUB_GITHUB_ORG:-}" ]]; then
    HELM_SETS+=(
      --set-string "adapter.hub.github.org=${HUB_GITHUB_ORG}"
      --set-string "adapter.hub.github.existingSecret=${HUB_GITHUB_SECRET}"
    )
  else
    # No GitHub org: keep desired state on a volume that outlives the pod.
    HELM_SETS+=(
      --set-string adapter.hub.gitDir=/var/lib/hub-git
      --set adapter.hub.persistence.enabled=true
    )
    if [[ -n "${HUB_GIT_URL:-}" ]]; then
      HELM_SETS+=(--set-string "adapter.hub.gitURL=${HUB_GIT_URL}")
    fi
  fi
  if [[ -n "${HUB_CLUSTER_REGION:-}" ]]; then
    HELM_SETS+=(--set-string "adapter.hub.clusterRegion=${HUB_CLUSTER_REGION}")
  fi
}
