#!/usr/bin/env bash
#
# Bring up the local lab, or install a hub onto a cluster that already exists.
#
#   scripts/up.sh                              kind hub and one connected environment
#   scripts/up.sh --down                       delete the lab
#   scripts/up.sh --kubeconfig PATH --hub --hub-public-url URL
#   scripts/up.sh --no-build                   reuse images already in the cluster
#
# With no cluster flags this is the lab: a hub and one kind cluster, joined
# through Connect with --local. --hub installs this checkout as the
# hub on an existing cluster: UI and adapter, no
# database platform. A kubeconfig without --hub is rejected.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${REPO_ROOT}/.local"
CHART_DIR="${REPO_ROOT}/charts/pds"
# helm_install_upgrade reads this from the caller.
# shellcheck disable=SC2034
CHART_REF="${CHART_DIR}"

RELEASE="${RELEASE:-pds}"
EVEREST_NAMESPACE="${EVEREST_NAMESPACE:-everest-system}"
UI_PORT="${UI_PORT:-18080}"
TAG="${TAG:-dev}"
ADAPTER_IMAGE="${ADAPTER_IMAGE:-ghcr.io/percona/percona-services-adapter}"
UI_IMAGE="${UI_IMAGE:-ghcr.io/percona/percona-services-ui}"
IMAGE_REGISTRY="${IMAGE_REGISTRY:-}"
PLATFORM_TIMEOUT="${PLATFORM_TIMEOUT:-900s}"

BUILD=true
DOWN=false
KUBE_URL="${KUBE_URL:-}"
KUBE_CONTEXT="${KUBE_CONTEXT:-}"
KUBECONFIG_FILE="${KUBECONFIG_FILE:-}"
INSECURE=false
INGRESS_HOST="${INGRESS_HOST:-}"
INGRESS_CLASS="${INGRESS_CLASS:-nginx}"
HUB=false
HUB_PUBLIC_URL="${HUB_PUBLIC_URL:-}"
HUB_GITHUB_ORG="${HUB_GITHUB_ORG:-}"
HUB_GITHUB_SECRET="${HUB_GITHUB_SECRET:-}"
HUB_GIT_URL="${HUB_GIT_URL:-}"
HUB_CLUSTER_REGION="${HUB_CLUSTER_REGION:-}"

# shellcheck source=lib/lab.sh
source "${REPO_ROOT}/scripts/lib/lab.sh"
# shellcheck source=lib/hub-install.sh
source "${REPO_ROOT}/scripts/lib/hub-install.sh"

up_parse_args "$@"

if [[ -n "${KUBECONFIG_FILE}" ]]; then
  [[ -f "${KUBECONFIG_FILE}" ]] || {
    echo "kubeconfig not found: ${KUBECONFIG_FILE}" >&2
    exit 2
  }
  KUBECONFIG_FILE="$(cd "$(dirname -- "${KUBECONFIG_FILE}")" && pwd)/$(basename -- "${KUBECONFIG_FILE}")"
  export KUBECONFIG="${KUBECONFIG_FILE}"
fi

up_mark_remote
up_validate

KUBECTL_ARGS=()
HELM_KUBE_ARGS=()
if [[ -n "${KUBECONFIG_FILE}" ]]; then
  KUBECTL_ARGS+=(--kubeconfig "${KUBECONFIG_FILE}")
  HELM_KUBE_ARGS+=(--kubeconfig "${KUBECONFIG_FILE}")
fi
if [[ -n "${KUBE_CONTEXT}" ]]; then
  KUBECTL_ARGS+=(--context "${KUBE_CONTEXT}")
  HELM_KUBE_ARGS+=(--kube-context "${KUBE_CONTEXT}")
fi
if [[ -n "${KUBE_URL}" ]]; then
  KUBECTL_ARGS+=(--server "${KUBE_URL}")
  HELM_KUBE_ARGS+=(--kube-apiserver "${KUBE_URL}")
fi
if [[ "${INSECURE}" == true ]]; then
  KUBECTL_ARGS+=(--insecure-skip-tls-verify)
  HELM_KUBE_ARGS+=(--kube-insecure-skip-tls-verify)
fi

kube() { helm_install_kube "$@"; }
helm_kube() { helm_install_helm "$@"; }
step() { printf '\n== %s\n' "$1"; }

# Docker image names cannot carry a scheme or a trailing slash.
# "http://labbox.example/" becomes "labbox.example".
normalize_image_registry() {
  [[ -z "${IMAGE_REGISTRY}" ]] && return 0
  IMAGE_REGISTRY="${IMAGE_REGISTRY#http://}"
  IMAGE_REGISTRY="${IMAGE_REGISTRY#https://}"
  IMAGE_REGISTRY="${IMAGE_REGISTRY%/}"
}

# GHCR packages default to private. Cluster nodes pull with this Secret so a
# first remote install works before the packages are made public.
# The GitHub token is not a Helm value. The Secret has to exist before install.
require_hub_github_secret() {
  [[ -n "${HUB_GITHUB_ORG}" ]] || return 0
  local secret="${HUB_GITHUB_SECRET:-hub-github}"
  kube get namespace "${EVEREST_NAMESPACE}" >/dev/null 2>&1 || \
    kube create namespace "${EVEREST_NAMESPACE}"
  if ! kube -n "${EVEREST_NAMESPACE}" get secret "${secret}" >/dev/null 2>&1; then
    echo "Secret ${secret} is missing in ${EVEREST_NAMESPACE}." >&2
    echo "Create it with key HUB_GITHUB_TOKEN, then rerun." >&2
    exit 2
  fi
}

ensure_ghcr_pull_secret() {
  case "${ADAPTER_IMAGE}" in
    ghcr.io/*) ;;
    *) return 0 ;;
  esac
  command -v gh >/dev/null 2>&1 || {
    echo "GHCR images are private until you make the packages public. Install gh or create an imagePullSecret." >&2
    return 0
  }
  local token user secret_name
  token="$(gh auth token 2>/dev/null || true)"
  [[ -n "${token}" ]] || {
    echo "GHCR images are private until you make the packages public, or run gh auth login." >&2
    return 0
  }
  user="${ADAPTER_IMAGE#ghcr.io/}"
  user="${user%%/*}"
  secret_name="pds-registry"
  kube get namespace "${EVEREST_NAMESPACE}" >/dev/null 2>&1 || \
    kube create namespace "${EVEREST_NAMESPACE}"
  kube -n "${EVEREST_NAMESPACE}" create secret docker-registry "${secret_name}" \
    --docker-server=ghcr.io \
    --docker-username="${user}" \
    --docker-password="${token}" \
    --dry-run=client -o yaml | kube -n "${EVEREST_NAMESPACE}" apply -f - >/dev/null
  HELM_SETS+=(--set "imagePullSecrets[0].name=${secret_name}")
  echo "image pull secret ${secret_name} (ghcr.io/${user})"
}

remote_target() {
  if [[ -n "${KUBECONFIG_FILE}" ]]; then
    printf '%s' "${KUBECONFIG_FILE}"
    [[ -n "${KUBE_CONTEXT}" ]] && printf ' (context %s)' "${KUBE_CONTEXT}"
    return
  fi
  printf '%s' "${KUBE_URL:-${KUBE_CONTEXT:-remote}}"
}

down_invocation() {
  local cmd
  cmd="$(basename "$0")"
  [[ -n "${KUBECONFIG_FILE}" ]] && cmd+=" --kubeconfig ${KUBECONFIG_FILE}"
  [[ "${HUB}" == true ]] && cmd+=" --hub"
  [[ -n "${KUBE_URL}" ]] && cmd+=" --kube-url ${KUBE_URL}"
  [[ -n "${KUBE_CONTEXT}" ]] && cmd+=" --kube-context ${KUBE_CONTEXT}"
  [[ "${INSECURE}" == true ]] && cmd+=" --insecure"
  printf '%s --down' "${cmd}"
}

stop_ui_forward() {
  local pid_file="${STATE_DIR}/ui-forward.pid"
  [[ -f "${pid_file}" ]] || return 0
  local pid
  pid="$(cat "${pid_file}")"
  if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
  fi
  rm -f "${pid_file}"
}

start_ui_forward() {
  stop_ui_forward
  mkdir -p "${STATE_DIR}"
  local quoted
  quoted="$(printf '%q ' "${KUBECTL_ARGS[@]}")"
  nohup bash -c "
while true; do
  kubectl ${quoted} -n '${EVEREST_NAMESPACE}' --address 127.0.0.1 \
    port-forward 'svc/${RELEASE}-ui' '${UI_PORT}:8080' \
    >>'${STATE_DIR}/ui-forward.log' 2>&1 || true
  sleep 1
done
" >/dev/null 2>&1 &
  echo $! >"${STATE_DIR}/ui-forward.pid"

  local ready=false
  local _
  for _ in $(seq 1 30); do
    if curl --silent --fail --max-time 2 "http://127.0.0.1:${UI_PORT}/nginx-health" >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  if [[ "${ready}" != true ]]; then
    echo "the UI did not answer on 127.0.0.1:${UI_PORT}. See ${STATE_DIR}/ui-forward.log" >&2
    cat "${STATE_DIR}/ui-forward.log" >&2 || true
    exit 1
  fi
}

build_and_push_remote() {
  local platform_args=()
  case "$(uname -m)" in
    arm64|aarch64) platform_args+=(--platform linux/amd64) ;;
  esac
  step "building images for the remote cluster"
  docker build --provenance=false "${platform_args[@]}" \
    -f "${REPO_ROOT}/adapter/Dockerfile" \
    -t "${ADAPTER_IMAGE}:${TAG}" "${REPO_ROOT}"
  (cd "${REPO_ROOT}" && npm -w @pds/ui run build)
  docker build --provenance=false "${platform_args[@]}" \
    -t "${UI_IMAGE}:${TAG}" "${REPO_ROOT}/apps/ui"
  step "pushing images to ${IMAGE_REGISTRY}"
  docker tag "${ADAPTER_IMAGE}:${TAG}" "${IMAGE_REGISTRY}/pds-adapter:${TAG}"
  docker tag "${UI_IMAGE}:${TAG}" "${IMAGE_REGISTRY}/pds-ui:${TAG}"
  docker push "${IMAGE_REGISTRY}/pds-adapter:${TAG}"
  docker push "${IMAGE_REGISTRY}/pds-ui:${TAG}"
  ADAPTER_IMAGE="${IMAGE_REGISTRY}/pds-adapter"
  UI_IMAGE="${IMAGE_REGISTRY}/pds-ui"
}

if [[ "$(up_mode)" == "lab" ]]; then
  if [[ "${DOWN}" == true ]]; then
    lab_down
    exit 0
  fi
  lab_up
  exit 0
fi

if [[ "$(up_mode)" != "hub" ]]; then
  echo "installing onto an existing cluster needs --hub. With no flags this command is the lab." >&2
  exit 2
fi

if [[ "${DOWN}" == true ]]; then
  stop_ui_forward
  command -v kubectl >/dev/null || { echo "missing required tool: kubectl" >&2; exit 1; }
  command -v helm >/dev/null || { echo "missing required tool: helm" >&2; exit 1; }
  step "uninstalling ${RELEASE} from the remote cluster"
  helm_install_uninstall
  echo "left the Kubernetes cluster in place"
  exit 0
fi

for tool in kubectl helm curl; do
  command -v "${tool}" >/dev/null || { echo "missing required tool: ${tool}" >&2; exit 1; }
done
normalize_image_registry
# Learn an installed ingress host, then reject a hub that cannot be reached
# or whose GitHub Secret is missing, before building images.
helm_install_preserve_ingress
hub_install_resolve || exit 2
require_hub_github_secret
if [[ "${BUILD}" == true ]]; then
  for tool in docker npm; do
    command -v "${tool}" >/dev/null || { echo "missing required tool: ${tool}" >&2; exit 1; }
  done
  docker info >/dev/null 2>&1 || { echo "docker daemon is not reachable" >&2; exit 1; }
  if [[ -z "${IMAGE_REGISTRY}" ]]; then
    echo "a remote cluster cannot kind-load images. Set IMAGE_REGISTRY (for example gcr.io/<project>) or pass --no-build." >&2
    exit 2
  fi
  build_and_push_remote
elif [[ -n "${IMAGE_REGISTRY}" ]]; then
  ADAPTER_IMAGE="${IMAGE_REGISTRY}/pds-adapter"
  UI_IMAGE="${IMAGE_REGISTRY}/pds-ui"
fi

helm_install_preflight
mkdir -p "${STATE_DIR}"

echo "hub mode: this cluster does not run databases or a backup store"

# Login is required, and nobody can click an invite link on a laptop, so this
# install seeds the development organization the chart knows a password for. It is
# off in the chart by default and belongs nowhere near a customer deployment.
BOOTSTRAP_ORGANIZATION="${BOOTSTRAP_ORGANIZATION:-dev}"
BOOTSTRAP_PASSWORD="${BOOTSTRAP_PASSWORD:-percona-dev-login}"

HELM_SETS=(
  --set "adapter.image.repository=${ADAPTER_IMAGE}"
  --set "adapter.image.tag=${TAG}"
  --set "ui.image.repository=${UI_IMAGE}"
  --set "ui.image.tag=${TAG}"
  --set adapter.auth.bootstrapOrganization.enabled=true
  --set-string "adapter.auth.bootstrapOrganization.password=${BOOTSTRAP_PASSWORD}"
  # The seeded organization is the organization the adapter serves. They cannot differ: a
  # session for any other organization reads as signed out, so seeding a second id
  # would write a password that signs in and is refused on the next request.
  --set-string "adapter.state.organization=${BOOTSTRAP_ORGANIZATION}"
)
# Preserve the installed host before deriving a hub public URL from it.
helm_install_preserve_ingress
helm_install_apply_ingress_sets
hub_install_prepare || exit 2

ensure_ghcr_pull_secret
# A remote registry reuses the :dev tag, so IfNotPresent keeps yesterday's
# adapter. Always pull when we pointed Helm at IMAGE_REGISTRY, including
# --no-build after a push.
if [[ "${BUILD}" == true || -n "${IMAGE_REGISTRY}" ]]; then
  HELM_SETS+=(
    --set adapter.image.pullPolicy=Always
    --set ui.image.pullPolicy=Always
  )
fi

if [[ -n "${SN_API_KEY:-}" ]]; then
  HELM_SETS+=(
    --set servicenow.enabled=true
    --set-string "servicenow.apiKey=${SN_API_KEY}"
  )
  if [[ -n "${SN_CLIENT_TOKEN:-}" ]]; then
    HELM_SETS+=(--set-string "servicenow.clientToken=${SN_CLIENT_TOKEN}")
  fi
  if [[ -n "${SN_URL:-}" ]]; then
    HELM_SETS+=(--set-string "servicenow.url=${SN_URL}")
  fi
fi

step "helm dependencies"
"${REPO_ROOT}/scripts/helm-deps.sh"

step "installing ${RELEASE} onto $(remote_target)"
helm_install_upgrade

helm_install_wait

ingress_line="off (port-forward only)"
if [[ -n "${INGRESS_HOST}" ]]; then
  if [[ -n "${INGRESS_TLS_SECRET:-}" ]]; then
    ingress_line="https://${INGRESS_HOST} (class ${INGRESS_CLASS})"
  else
    ingress_line="http://${INGRESS_HOST} (class ${INGRESS_CLASS})"
  fi
fi

step "UI port-forward on 127.0.0.1:${UI_PORT}"
start_ui_forward

if [[ -n "${HUB_GITHUB_ORG}" ]]; then
  state_line="GitHub org ${HUB_GITHUB_ORG} (Secret ${HUB_GITHUB_SECRET})"
else
  state_line="git directory /var/lib/hub-git on a persistent volume"
  if [[ -n "${HUB_GIT_URL}" ]]; then
    state_line="${state_line} (clone ${HUB_GIT_URL})"
  fi
fi
cat <<EOF

Percona Database Service hub is running on $(remote_target).

  UI:                 http://127.0.0.1:${UI_PORT}/app/
  Sign in:            ${BOOTSTRAP_ORGANIZATION} / ${BOOTSTRAP_PASSWORD}
  Helm release:       ${RELEASE} in ${EVEREST_NAMESPACE}
  Public URL:         ${HUB_PUBLIC_URL}
  Desired state:      ${state_line}
  Ingress:            ${ingress_line}
  Database platform:  off
  Backup store:       off (each environment keeps its own backups)

Sign in, then use Create an environment for each Kubernetes cluster you
connect. This command did not create the Kubernetes cluster.

That organization exists because this is a development install. On a real deployment
Percona creates one and mints a single use invite link instead:

  kubectl exec -n ${EVEREST_NAMESPACE} deploy/${RELEASE}-adapter -- adapter organization create acme

Stop the UI tunnel:   kill \$(cat ${STATE_DIR}/ui-forward.pid)
Tear this install down: $(down_invocation)
EOF
