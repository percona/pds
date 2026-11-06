#!/usr/bin/env bash
#
# Shared Helm install helpers for the pds umbrella.
#
# Sourced by scripts/lib/lab.sh, which scripts/up.sh loads.
# Callers set RELEASE, EVEREST_NAMESPACE, CHART_REF, PLATFORM_TIMEOUT,
# and the KUBECTL_ARGS / HELM_KUBE_ARGS arrays.
# Extra chart values go in the HELM_SETS array.
#
# shellcheck shell=bash

if ! declare -p KUBECTL_ARGS >/dev/null 2>&1; then
  declare -a KUBECTL_ARGS=()
fi
if ! declare -p HELM_KUBE_ARGS >/dev/null 2>&1; then
  declare -a HELM_KUBE_ARGS=()
fi
if ! declare -p HELM_VALUE_FILES >/dev/null 2>&1; then
  declare -a HELM_VALUE_FILES=()
fi

helm_install_require_tools() {
  local tool
  for tool in kubectl helm; do
    command -v "${tool}" >/dev/null || {
      echo "missing required tool: ${tool}" >&2
      return 1
    }
  done
}

helm_install_kube() {
  if ((${#KUBECTL_ARGS[@]})); then
    kubectl "${KUBECTL_ARGS[@]}" "$@"
  else
    kubectl "$@"
  fi
}

helm_install_helm() {
  if ((${#HELM_KUBE_ARGS[@]})); then
    helm "${HELM_KUBE_ARGS[@]}" "$@"
  else
    helm "$@"
  fi
}

helm_install_supports_force_conflicts() {
  helm upgrade --help 2>/dev/null | grep -q -- '--force-conflicts'
}

# How to ask Helm to wait. Helm 3 has one behaviour; Helm 4 made it a choice of
# strategies and made a bare --wait mean "watcher", which judges every resource
# including CustomResourceDefinitions. A CRD never resolves for that watcher, so
# a chart carrying operator CRDs waits out the whole timeout and then fails with
# each CRD listed as not ready. "legacy" is the Helm 3 behaviour under a name,
# and Helm 3 rejects the argument, so it only goes on when Helm 4 answers.
helm_install_wait_flag() {
  local version
  version="$(helm version --short 2>/dev/null || true)"
  case "${version}" in
    v4* | 4*) echo "--wait=legacy" ;;
    *) echo "--wait" ;;
  esac
}

# Fail before mutating the cluster when kubectl, a default StorageClass, or
# cluster-info is missing.
helm_install_preflight() {
  helm_install_require_tools || return 1
  if ! helm_install_kube cluster-info >/dev/null 2>&1; then
    echo "kubectl cannot reach the cluster. Check kubeconfig and context." >&2
    return 1
  fi
  local sc
  sc="$(helm_install_kube get storageclass -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  if [[ -z "${sc}" ]]; then
    echo "cluster has no default StorageClass. Volumes need one." >&2
    return 1
  fi
}

# If INGRESS_HOST is empty, keep the host the installed release already serves.
helm_install_preserve_ingress() {
  local ns="${EVEREST_NAMESPACE:-everest-system}"
  local release="${RELEASE:-pds}"
  if [[ -n "${INGRESS_HOST:-}" ]]; then
    return 0
  fi
  local installed_ingress installed_host installed_class
  installed_ingress="$(helm_install_kube -n "${ns}" get ingress "${release}" \
    -o jsonpath='{.spec.rules[0].host} {.spec.ingressClassName}' 2>/dev/null || true)"
  installed_host="${installed_ingress%% *}"
  if [[ -n "${installed_host}" ]]; then
    INGRESS_HOST="${installed_host}"
    installed_class="${installed_ingress##* }"
    if [[ -n "${installed_class}" ]]; then
      INGRESS_CLASS="${installed_class}"
    fi
    echo "ingress: keeping ${INGRESS_HOST} (class ${INGRESS_CLASS:-}) from the installed release"
  fi
}

helm_install_apply_ingress_sets() {
  if [[ -z "${INGRESS_HOST:-}" ]]; then
    return 0
  fi
  HELM_SETS+=(
    --set ingress.enabled=true
    --set-string "ingress.className=${INGRESS_CLASS:-nginx}"
    --set-string "ingress.host=${INGRESS_HOST}"
  )
  if [[ -n "${INGRESS_TLS_SECRET:-}" ]]; then
    HELM_SETS+=(
      --set ingress.tls.enabled=true
      --set-string "ingress.tls.secretName=${INGRESS_TLS_SECRET}"
    )
  fi
}

helm_install_upgrade() {
  local ns="${EVEREST_NAMESPACE:?EVEREST_NAMESPACE is required}"
  local release="${RELEASE:?RELEASE is required}"
  local chart="${CHART_REF:?CHART_REF is required}"
  local timeout="${PLATFORM_TIMEOUT:-900s}"
  local extra=()
  if helm_install_supports_force_conflicts; then
    extra+=(--force-conflicts)
  fi
  echo "helm upgrade --install ${release} ${chart} --namespace ${ns}"
  helm_install_helm upgrade --install "${release}" "${chart}" \
    --namespace "${ns}" --create-namespace \
    "${extra[@]+"${extra[@]}"}" \
    --timeout "${timeout}" \
    "${HELM_VALUE_FILES[@]+"${HELM_VALUE_FILES[@]}"}" \
    "${HELM_SETS[@]+"${HELM_SETS[@]}"}"
}

helm_install_wait() {
  local ns="${EVEREST_NAMESPACE:?}"
  local release="${RELEASE:?}"
  local timeout="${PLATFORM_TIMEOUT:-900s}"
  helm_install_kube -n "${ns}" rollout status "deployment/${release}-adapter" --timeout="${timeout}"
  helm_install_kube -n "${ns}" rollout status "deployment/${release}-ui" --timeout="${timeout}"
}

# Uninstall the control plane and leave the cluster in place.
helm_install_uninstall() {
  local ns="${EVEREST_NAMESPACE:?}"
  local release="${RELEASE:?}"
  helm_install_helm uninstall "${release}" -n "${ns}" \
    --ignore-not-found --no-hooks --timeout 120s || true
}
