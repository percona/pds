#!/usr/bin/env bash
#
# Local lab: a kind hub and one connected environment.
#
# The hub runs the chart defaults and values-hub-lab.yaml. Git lives on a host
# path and is served by Gitea so the connected environment can clone it. The
# cluster joins through the Connect command with --local. Nothing here installs
# a shared agent token.
#
# Sourced by scripts/up.sh. Pure functions are safe to call from tests.
#
# shellcheck shell=bash

_LAB_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=helm-install.sh
source "${_LAB_LIB_DIR}/helm-install.sh"
# shellcheck source=flux-environment.sh
source "${_LAB_LIB_DIR}/flux-environment.sh"

# Docker Desktop's 8 GB setting is the smallest that holds the hub and one
# connected environment. The daemon reports a little less than that slider, so the
# floor is 7 GiB and the message still says about 8 GB.
lab_min_memory_bytes() {
  echo $((7 * 1024 * 1024 * 1024))
}

lab_format_gib() {
  awk -v bytes="$1" 'BEGIN { printf "%.1f GiB", bytes / 1024 / 1024 / 1024 }'
}

lab_require_memory() {
  local bytes="$1"
  local min
  if ! [[ "${bytes}" =~ ^[0-9]+$ ]]; then
    echo "could not read Docker memory. docker info did not report a size." >&2
    return 1
  fi
  min="$(lab_min_memory_bytes)"
  if [[ "${bytes}" -lt "${min}" ]]; then
    echo "Docker has $(lab_format_gib "${bytes}") of memory. The lab needs about 8 GB. Raise the memory limit in Docker Desktop and rerun." >&2
    return 1
  fi
}

lab_port_open() {
  local port="$1"
  if declare -F lab_port_open_hook >/dev/null 2>&1; then
    lab_port_open_hook "${port}"
    return
  fi
  (exec 3<>"/dev/tcp/127.0.0.1/${port}") >/dev/null 2>&1
}

# A listener this lab started must not fail the next run's port check.
lab_port_owned() {
  local name="$1"
  if declare -F lab_port_owned_hook >/dev/null 2>&1; then
    lab_port_owned_hook "${name}"
    return
  fi
  return 1
}

lab_check_ports() {
  local spec name port rc
  rc=0
  for spec in "$@"; do
    name="${spec%%:*}"
    port="${spec##*:}"
    if lab_port_open "${port}"; then
      if lab_port_owned "${name}"; then
        continue
      fi
      echo "port ${port} is already in use (${name}). Free it, or set ${name} to another port." >&2
      rc=1
    fi
  done
  return "${rc}"
}

lab_shell_quote() {
  local value="$1"
  local q="'"
  local escaped="${value//${q}/${q}\\${q}${q}}"
  printf '%s%s%s' "${q}" "${escaped}" "${q}"
}

# The command the hub would print, plus --local, so the script joins the
# cluster kubectl already points at and creates nothing in AWS.
lab_join_command() {
  local hub="$1"
  local token="$2"
  if [[ -z "${hub}" || -z "${token}" ]]; then
    echo "a join command needs the hub address and a token" >&2
    return 1
  fi
  hub="${hub%/}"
  printf 'curl -fsSL %s | bash -s -- --token %s --local' \
    "$(lab_shell_quote "${hub}/api/internal/connect/bootstrap.sh")" \
    "$(lab_shell_quote "${token}")"
}

lab_join_command_local() {
  local command="$1"
  if [[ -z "${command}" ]]; then
    echo "the hub did not return a Connect command" >&2
    return 1
  fi
  case "${command}" in
    *"--local"*) printf '%s' "${command}" ;;
    *) printf '%s --local' "${command}" ;;
  esac
}

# The command the hub prints for a token, with --update in front of --token.
# An update works on the Kubernetes cluster kubectl points at and takes no other
# flag, so --local does not belong here.
lab_update_command() {
  local command="$1"
  if [[ -z "${command}" ]]; then
    echo "the hub did not return a Connect command" >&2
    return 1
  fi
  if [[ "${command}" != *"--token"* ]]; then
    echo "the Connect command carries no --token to update with" >&2
    return 1
  fi
  printf '%s' "${command/--token/--update --token}"
}

lab_connect_command() {
  local api_command="$1"
  local hub_url="$2"
  local token="$3"
  if [[ -n "${api_command}" ]]; then
    lab_join_command_local "${api_command}"
    return
  fi
  lab_join_command "${hub_url}" "${token}"
}

# A seeded sign-in only works for one named organization. "*" fails the chart render.
lab_valid_organization() {
  local id="$1"
  [[ "${#id}" -ge 3 ]] || return 1
  [[ "${id}" =~ ^[a-z]([-a-z0-9]*[a-z0-9])?$ ]]
}

lab_check_bootstrap() {
  local password="${BOOTSTRAP_PASSWORD:-}"
  if ! lab_valid_organization "${BOOTSTRAP_ORGANIZATION:-}"; then
    echo "the lab organization must be one named id. A star cannot be seeded." >&2
    return 1
  fi
  if [[ "${#password}" -lt 12 ]]; then
    echo "the lab sign-in password needs at least 12 characters" >&2
    return 1
  fi
  # Helm reads --set-string as key=value, so the password cannot carry the
  # separators that would split that argument.
  if [[ "${password}" == *[[:space:]]* || "${password}" == *"'"* || "${password}" == *","* || "${password}" == *"="* ]]; then
    echo "the lab sign-in password must be a single token" >&2
    return 1
  fi
}

# Kind reaches the host by this name. 127.0.0.1 inside a pod is the pod.
lab_public_origin() {
  printf 'http://%s:%s' "$1" "$2"
}

lab_git_clone_url() {
  local host="$1"
  local port="$2"
  local user="$3"
  local repo="$4"
  printf 'http://%s:%s/%s/%s.git' "${host}" "${port}" "${user}" "${repo}"
}

lab_arm64_note() {
  case "$1" in
    arm64 | aarch64)
      printf '%s\n' "This machine is $1, so the connected environment runs without monitoring. The lab continues."
      ;;
  esac
}

# The platform Kustomization is where monitoring is installed. The Connect
# script does not create it when the cluster has no amd64 node.
lab_wants_platform_kustomization() {
  case "$1" in
    arm64 | aarch64) return 1 ;;
    *) return 0 ;;
  esac
}

lab_fill_hub_helm_sets() {
  local public_url="$1"
  local git_url="$2"
  local wait_flag="$3"
  lab_check_bootstrap || return 1
  HELM_SETS+=(
    --namespace "${HUB_NS:-everest-system}"
    --create-namespace
    -f "${CHART_DIR}/values-hub-lab.yaml"
    --set "adapter.image.repository=${ADAPTER_IMAGE}"
    --set "adapter.image.tag=${TAG}"
    --set "ui.image.repository=${UI_IMAGE}"
    --set "ui.image.tag=${TAG}"
    --set adapter.auth.bootstrapOrganization.enabled=true
    --set-string "adapter.auth.bootstrapOrganization.password=${BOOTSTRAP_PASSWORD}"
    --set-string "adapter.state.organization=${BOOTSTRAP_ORGANIZATION}"
    --set-string "adapter.hub.publicURL=${public_url}"
    --set-string "adapter.hub.oauthIssuer=http://127.0.0.1:${UI_PORT}"
    --set-string "adapter.hub.gitURL=${git_url}"
    "${wait_flag}"
    --timeout
    15m
  )
}

lab_namespace_action() {
  case "$1" in
    "" | Active) printf '%s' ok ;;
    Terminating) printf '%s' wait ;;
    *) printf '%s' fail ;;
  esac
}

lab_environment_status() {
  local json="$1"
  local name="$2"
  if [[ -z "${json}" ]]; then
    return 0
  fi
  printf '%s' "${json}" | python3 -c '
import json, sys
name = sys.argv[1]
try:
    data = json.load(sys.stdin)
except json.JSONDecodeError:
    sys.exit(0)
for item in data.get("environments") or []:
    if item.get("name") == name:
        sys.stdout.write(item.get("status") or "")
        break
' "${name}" 2>/dev/null || true
}

# The agent posts about every 15s. The hub calls an environment unreachable 90s
# after the last beat. A sample that is merely "connected" can be a beat
# that expires before the next check, which is what a rerun sees after the
# hub process restarts and no new beat has arrived.
lab_heartbeat_fresh_seconds=45

lab_environment_fresh() {
  local json="$1"
  local name="$2"
  local now="$3"
  if [[ -z "${json}" ]]; then
    return 0
  fi
  printf '%s' "${json}" | python3 -c '
import json, sys
from datetime import datetime, timezone
name, now, limit = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
try:
    data = json.load(sys.stdin)
except json.JSONDecodeError:
    sys.exit(0)
for item in data.get("environments") or []:
    if item.get("name") != name:
        continue
    status = item.get("status") or ""
    if status != "connected":
        sys.stdout.write(status)
        break
    seen = item.get("lastSeen") or ""
    if not seen:
        sys.stdout.write("unreachable")
        break
    at = datetime.fromisoformat(seen.replace("Z", "+00:00"))
    if at.tzinfo is None:
        at = at.replace(tzinfo=timezone.utc)
    age = now - int(at.timestamp())
    sys.stdout.write("connected" if age <= limit else "unreachable")
    break
' "${name}" "${now}" "${lab_heartbeat_fresh_seconds}" 2>/dev/null || true
}

lab_login_body() {
  python3 -c 'import json,sys; sys.stdout.write(json.dumps({"username": sys.argv[1], "password": sys.argv[2]}))' "$1" "$2"
}

lab_coredns_with_host() {
  local corefile="$1"
  local ip="$2"
  local name="$3"
  if printf '%s\n' "${corefile}" | grep -F -q "${name}"; then
    # A rerun may see a new gateway. Replace the address; do not add a second hosts block.
    printf '%s\n' "${corefile}" | awk -v ip="${ip}" -v name="${name}" '
      index($0, name) && $0 ~ /[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/ {
        sub(/[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/, ip)
      }
      { print }
    '
    return 0
  fi
  printf '%s\n' "${corefile}" | awk -v ip="${ip}" -v name="${name}" '
    $0 ~ /^[[:space:]]*\.:53 \{/ && !done {
      print
      print "    hosts {"
      print "        " ip " " name
      print "        fallthrough"
      print "    }"
      done = 1
      next
    }
    { print }
  '
}

# docker inspect on Docker Desktop prefixes the host path with /host_mnt.
lab_mount_matches() {
  local listing="$1"
  local host_path="$2"
  local container_path="$3"
  local line source dest
  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    source="${line% *}"
    dest="${line##* }"
    [[ "${dest}" == "${container_path}" ]] || continue
    if [[ "${source}" == "${host_path}" || "${source}" == "/host_mnt${host_path}" ]]; then
      return 0
    fi
  done <<<"${listing}"
  return 1
}

lab_require_git_mount() {
  local cluster="$1"
  local host_path="$2"
  local listing
  listing="$(docker inspect "${cluster}-control-plane" --format '{{range .Mounts}}{{.Source}} {{.Destination}}{{"\n"}}{{end}}' 2>/dev/null || true)"
  if lab_mount_matches "${listing}" "${host_path}" /var/lab-git; then
    return 0
  fi
  echo "kind cluster ${cluster} exists but does not mount ${host_path} at /var/lab-git." >&2
  echo "It was not created for this lab. Delete that cluster yourself, then rerun. This command will not delete it." >&2
  return 1
}

# workers is how many extra nodes to add. Zero is the laptop default: one
# control plane. A scale to three members needs a node for each member, so the
# hub smoke passes a count and the laptop does not.
lab_kind_config() {
  local name="$1"
  local extra="$2"
  local workers="${3:-0}"
  local i
  if [[ ! "${workers}" =~ ^[0-9]+$ ]]; then
    echo "kind worker count must be a number, got ${workers}" >&2
    return 1
  fi
  cat <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: ${name}
nodes:
  - role: control-plane
    image: ${K8S_IMAGE:-kindest/node:v1.32.2}
${extra}
YAML
  for ((i = 0; i < workers; i++)); do
    printf '  - role: worker\n    image: %s\n' "${K8S_IMAGE:-kindest/node:v1.32.2}"
  done
}

lab_tool_list() {
  local build="$1"
  printf '%s\n' docker kind kubectl helm flux git curl python3
  if [[ "${build}" == true ]]; then
    printf '%s\n' node npm
  fi
}

lab_require_tools() {
  local build="$1"
  local tool hint
  while IFS= read -r tool; do
    hint=""
    case "${tool}" in
      flux) hint="https://fluxcd.io/flux/installation/" ;;
      node | npm) hint="or pass --no-build" ;;
    esac
    if ! command -v "${tool}" >/dev/null 2>&1; then
      if [[ -n "${hint}" ]]; then
        echo "missing required tool: ${tool} (${hint})" >&2
      else
        echo "missing required tool: ${tool}" >&2
      fi
      return 1
    fi
  done < <(lab_tool_list "${build}")
}

lab_signin_lines() {
  local organization="$1"
  local password="$2"
  local ui_port="$3"
  cat <<EOF
  UI:                 http://127.0.0.1:${ui_port}/app/
  Sign in:            ${organization} / ${password}
  These credentials are throwaway. Anyone who can reach this UI can sign in with them.
EOF
}

up_usage() {
  cat <<EOF >&2
usage: $(basename "$0") [--no-build] [--down]
       $(basename "$0") --kubeconfig PATH --hub [--kube-context NAME] [--kube-url URL]
       [--insecure] [--ingress-host HOST] [--ingress-class NAME]
       [--hub-public-url URL] [--hub-github-org ORG]
       [--hub-github-secret NAME] [--hub-git-url URL] [--hub-region REGION]
       [--no-build] [--down]

  no flags               lab: a kind hub and one connected environment, joined with Connect --local
  --no-build             reuse images already loaded into the kind nodes
  --down                 lab: delete both kind clusters, Gitea, and port-forwards
                         with --kubeconfig --hub: uninstall the release and stop the tunnel

  --kubeconfig PATH      kubeconfig of an existing cluster; requires --hub
  --kube-context NAME    context inside the kubeconfig (default: current-context)
  --kube-url URL         optional API server override (credentials still from kubeconfig)
  --hub                  install this checkout as a hub on that cluster
  --hub-public-url URL   origin clusters use to reach the hub (or pass --ingress-host)
  --hub-github-org ORG   one private GitHub repo per environment; needs a Secret
  --hub-github-secret NAME  Secret holding HUB_GITHUB_TOKEN (default hub-github)
  --hub-git-url URL      clone URL for the git directory backend
  --hub-region REGION    default region printed on the Connect command
  --ingress-host HOST    serve the UI at / and the adapter at /api on that host
  --ingress-class NAME   ingress class for that host (default nginx)
  --insecure             skip TLS verification of the API server

  IMAGE_REGISTRY      registry prefix only, no scheme (ghcr.io/<user>)
EOF
  exit 2
}

up_parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --no-build) BUILD=false ;;
      --down) DOWN=true ;;
      --insecure) INSECURE=true ;;
      --kube-url)
        [[ $# -ge 2 ]] || up_usage
        KUBE_URL="$2"
        shift
        ;;
      --kube-context)
        [[ $# -ge 2 ]] || up_usage
        KUBE_CONTEXT="$2"
        shift
        ;;
      --kubeconfig)
        [[ $# -ge 2 ]] || up_usage
        KUBECONFIG_FILE="$2"
        shift
        ;;
      --ingress-host)
        [[ $# -ge 2 ]] || up_usage
        INGRESS_HOST="$2"
        shift
        ;;
      --ingress-class)
        [[ $# -ge 2 ]] || up_usage
        INGRESS_CLASS="$2"
        shift
        ;;
      --hub) HUB=true ;;
      --hub-public-url)
        [[ $# -ge 2 ]] || up_usage
        HUB_PUBLIC_URL="$2"
        shift
        ;;
      --hub-github-org)
        [[ $# -ge 2 ]] || up_usage
        HUB_GITHUB_ORG="$2"
        shift
        ;;
      --hub-github-secret)
        [[ $# -ge 2 ]] || up_usage
        HUB_GITHUB_SECRET="$2"
        shift
        ;;
      --hub-git-url)
        [[ $# -ge 2 ]] || up_usage
        HUB_GIT_URL="$2"
        shift
        ;;
      --hub-region)
        [[ $# -ge 2 ]] || up_usage
        HUB_CLUSTER_REGION="$2"
        shift
        ;;
      -h | --help) up_usage ;;
      *) up_usage ;;
    esac
    shift
  done
  # up.sh decides teardown and the ingress class from these after parsing.
  export DOWN INGRESS_CLASS
}

up_mark_remote() {
  REMOTE=false
  if [[ -n "${KUBECONFIG_FILE}" || -n "${KUBE_URL}" || -n "${KUBE_CONTEXT}" ]]; then
    REMOTE=true
  fi
}

up_mode() {
  if [[ "${REMOTE}" == true && "${HUB}" == true ]]; then
    printf '%s' hub
  elif [[ "${REMOTE}" == true ]]; then
    printf '%s' remote
  else
    printf '%s' lab
  fi
}

up_validate() {
  if [[ "${HUB}" != true ]]; then
    if [[ -n "${HUB_PUBLIC_URL}" || -n "${HUB_GITHUB_ORG}" || -n "${HUB_GITHUB_SECRET}" || -n "${HUB_GIT_URL}" || -n "${HUB_CLUSTER_REGION}" ]]; then
      echo "hub settings need --hub" >&2
      return 2
    fi
  fi
  if [[ "${HUB}" == true && -z "${KUBECONFIG_FILE}" ]]; then
    echo "--hub installs onto an existing cluster. Pass --kubeconfig." >&2
    return 2
  fi
  if [[ "${REMOTE}" == true && "${HUB}" != true ]]; then
    echo "installing onto an existing cluster needs --hub. With no flags this command is the lab." >&2
    return 2
  fi
  if [[ "$(up_mode)" == "lab" ]]; then
    if [[ -n "${INGRESS_HOST}" || "${INSECURE}" == true ]]; then
      echo "that flag applies to a hub on an existing cluster. Pass --kubeconfig and --hub, or run with no arguments for the lab." >&2
      return 2
    fi
  fi
}

lab_root() {
  if [[ -n "${REPO_ROOT:-}" ]]; then
    printf '%s' "${REPO_ROOT}"
    return
  fi
  cd "${_LAB_LIB_DIR}/../.." && pwd
}

lab_stop_pid_file() {
  local file="$1"
  local pid
  [[ -f "${file}" ]] || return 0
  while IFS= read -r pid; do
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
      pkill -P "${pid}" 2>/dev/null || true
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  done <"${file}"
  rm -f "${file}"
}

lab_stop_helpers() {
  local state="$1"
  lab_stop_pid_file "${state}/ui-forward.pid"
  lab_stop_pid_file "${state}/lab-pf.pid"
  lab_stop_pid_file "${state}/lab-git-commit.pid"
  lab_stop_pid_file "${state}/lab-git-daemon.pid"
  lab_stop_pid_file "${state}/lab-git-http.pid"
  docker rm -f "${GITEA_CONTAINER:-lab-gitea}" >/dev/null 2>&1 || true
}

lab_wait_namespace() {
  local context="$1"
  local ns="$2"
  local phase action
  phase="$(kubectl --context "${context}" get namespace "${ns}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  action="$(lab_namespace_action "${phase}")"
  case "${action}" in
    ok) return 0 ;;
    wait)
      echo "namespace ${ns} is Terminating; waiting for it to finish"
      kubectl --context "${context}" wait --for=delete "namespace/${ns}" --timeout=180s
      ;;
    *)
      echo "namespace ${ns} is ${phase}" >&2
      return 1
      ;;
  esac
}

# grep -q exits at the first match and kind can take SIGPIPE writing the rest,
# which pipefail turns into "not found". Read the whole list first.
lab_kind_exists() {
  local clusters
  clusters="$(kind get clusters 2>/dev/null)" || true
  grep -qx "$1" <<<"${clusters}"
}

lab_create_kind() {
  local name="$1"
  local extra="$2"
  local workers="${3:-0}"
  if lab_kind_exists "${name}"; then
    echo "kind cluster ${name} already exists"
    return 0
  fi
  local config
  config="$(mktemp)"
  if ! lab_kind_config "${name}" "${extra}" "${workers}" >"${config}"; then
    rm -f "${config}"
    return 1
  fi
  kind create cluster --config "${config}" --wait 180s
  rm -f "${config}"
}

lab_valid_ipv4() {
  [[ "$1" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]
}

# getent hosts on Docker Desktop returns the IPv6 gateway first. Port-forward
# listens on IPv4, so the first address is the wrong one to pin in CoreDNS.
lab_first_ipv4() {
  awk '
    {
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^([0-9]{1,3}\.){3}[0-9]{1,3}$/) {
          print $i
          exit
        }
      }
    }
  '
}

lab_host_ip_from_answers() {
  local ahosts="$1"
  local hosts="$2"
  local ip
  ip="$(printf '%s\n' "${ahosts}" | lab_first_ipv4 || true)"
  if lab_valid_ipv4 "${ip}"; then
    printf '%s' "${ip}"
    return 0
  fi
  ip="$(printf '%s\n' "${hosts}" | lab_first_ipv4 || true)"
  if lab_valid_ipv4 "${ip}"; then
    printf '%s' "${ip}"
    return 0
  fi
  return 1
}

lab_host_ip_from_node() {
  local cluster="$1"
  local ahosts hosts
  ahosts="$(docker exec "${cluster}-control-plane" getent ahostsv4 host.docker.internal 2>/dev/null || true)"
  hosts="$(docker exec "${cluster}-control-plane" getent hosts host.docker.internal 2>/dev/null || true)"
  # No address is a normal result on a Linux kind node. Returning 1 here makes
  # set -e stop the caller before it can use the bridge gateway.
  lab_host_ip_from_answers "${ahosts}" "${hosts}" || true
}

# Pod DNS does not see a kind node's /etc/hosts. Pin the name in CoreDNS so
# the agent and Flux can reach Gitea and the hub on the host.
lab_pin_host_in_cluster() {
  local context="$1"
  local ip="$2"
  local current patched tmp
  current="$(kubectl --context "${context}" -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}')"
  patched="$(lab_coredns_with_host "${current}" "${ip}" host.docker.internal)"
  if [[ "${patched}" == "${current}" ]]; then
    return 0
  fi
  tmp="$(mktemp)"
  printf '%s\n' "${patched}" >"${tmp}"
  kubectl --context "${context}" -n kube-system create configmap coredns \
    --from-file="Corefile=${tmp}" \
    --dry-run=client -o yaml | kubectl --context "${context}" apply -f -
  rm -f "${tmp}"
  kubectl --context "${context}" -n kube-system rollout restart deploy/coredns >/dev/null
  kubectl --context "${context}" -n kube-system rollout status deploy/coredns --timeout=180s
}

lab_ensure_host_alias() {
  local cluster="$1"
  local ip
  ip="$(lab_host_ip_from_node "${cluster}")"
  if ! lab_valid_ipv4 "${ip}"; then
    if [[ "$(uname -s)" == "Linux" ]]; then
      ip="$(docker network inspect bridge -f '{{ (index .IPAM.Config 0).Gateway }}' 2>/dev/null || true)"
      if lab_valid_ipv4 "${ip}"; then
        docker exec "${cluster}-control-plane" sh -c "grep -q host.docker.internal /etc/hosts || echo '${ip} host.docker.internal' >> /etc/hosts"
      fi
    fi
  fi
  if ! lab_valid_ipv4 "${ip}"; then
    echo "kind cluster ${cluster} cannot resolve host.docker.internal, so the connected environment cannot reach the lab." >&2
    return 1
  fi
  lab_pin_host_in_cluster "kind-${cluster}" "${ip}"
}

lab_start_git_sync() {
  local git_dir="$1"
  local log="$2"
  local pidfile="$3"
  # The hub writes files and does not commit them. This loop publishes those
  # files to Gitea so Flux can clone them. The pause is the publish interval.
  nohup bash -c '
set -u
cd "$1" || exit 1
while true; do
  chmod -R a+rwX . 2>/dev/null || true
  git add -A
  if ! git diff --cached --quiet; then
    git -c user.email=lab@local -c user.name=hub commit -m "hub desired state" >/dev/null || true
    git push origin HEAD:main || echo "git push failed; Flux will not see this commit" >&2
  fi
  sleep 2
done
' _ "${git_dir}" >"${log}" 2>&1 &
  echo $! >"${pidfile}"
}

lab_start_gitea() {
  local state="$1"
  local git_dir="$2"
  local port="$3"
  local host_name="$4"
  local origin="http://${GITEA_USER}:${GITEA_PASSWORD}@127.0.0.1:${port}/${GITEA_USER}/${GITEA_REPO}.git"
  docker rm -f "${GITEA_CONTAINER}" >/dev/null 2>&1 || true
  docker run -d --name "${GITEA_CONTAINER}" \
    -p "${port}:3000" \
    -e GITEA__database__DB_TYPE=sqlite3 \
    -e GITEA__server__HTTP_PORT=3000 \
    -e GITEA__server__ROOT_URL="http://${host_name}:${port}/" \
    -e GITEA__server__DISABLE_SSH=true \
    -e GITEA__security__INSTALL_LOCK=true \
    -e GITEA__service__DISABLE_REGISTRATION=true \
    -e GITEA__service__REQUIRE_SIGNIN_VIEW=false \
    gitea/gitea:1.22 >/dev/null
  local start now
  start="$(date +%s)"
  while true; do
    if curl -sf "http://127.0.0.1:${port}/api/v1/version" >/dev/null 2>&1; then
      break
    fi
    now="$(date +%s)"
    if (( now - start >= 120 )); then
      echo "Gitea did not become ready on :${port}" >&2
      return 1
    fi
    sleep 2
  done
  docker exec -u git "${GITEA_CONTAINER}" gitea admin user create \
    --admin --username "${GITEA_USER}" --password "${GITEA_PASSWORD}" \
    --email lab@local --must-change-password=false >/dev/null 2>&1 || true
  curl -sf -u "${GITEA_USER}:${GITEA_PASSWORD}" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"${GITEA_REPO}\",\"private\":false,\"auto_init\":false}" \
    "http://127.0.0.1:${port}/api/v1/user/repos" >/dev/null || true
  git -C "${git_dir}" remote remove origin >/dev/null 2>&1 || true
  git -C "${git_dir}" remote add origin "${origin}"
  local attempt
  attempt=0
  while true; do
    if git -C "${git_dir}" -c http.sslVerify=false push -u origin HEAD:main >/dev/null; then
      return 0
    fi
    attempt=$((attempt + 1))
    if [[ "${attempt}" -ge 10 ]]; then
      echo "could not push the lab git tree to Gitea" >&2
      return 1
    fi
    sleep 2
  done
}

lab_prepare_git() {
  local git_dir="$1"
  local src="$2"
  if [[ -d "${git_dir}/.git" ]]; then
    echo "reusing the git tree at ${git_dir}"
    # The kind mount and the git sync write some files as root. chmod as the
    # lab user then fails, and a rerun would stop before it can refresh the tree.
    chmod -R a+rwX "${git_dir}" 2>/dev/null || sudo chmod -R a+rwX "${git_dir}"
    return 0
  fi
  rm -rf "${git_dir}"
  mkdir -p "${git_dir}"
  cp -R "${src}/." "${git_dir}/"
  git -C "${git_dir}" init -b main >/dev/null
  git -C "${git_dir}" -c user.email=lab@local -c user.name=hub add -A
  git -C "${git_dir}" -c user.email=lab@local -c user.name=hub commit -m "seed inventory" >/dev/null
  chmod -R a+rwX "${git_dir}"
}

# Bound on every interface. A forward that listens only on 127.0.0.1 is
# invisible to kind, which reaches the host as host.docker.internal.
lab_start_ui_forward() {
  local context="$1"
  local namespace="$2"
  local port="$3"
  local state="$4"
  lab_stop_pid_file "${state}/ui-forward.pid"
  mkdir -p "${state}"
  nohup bash -c '
while true; do
  kubectl --context "$1" -n "$2" --address 0.0.0.0 \
    port-forward svc/pds-ui "$3:8080" || true
  sleep 1
done
' _ "${context}" "${namespace}" "${port}" >>"${state}/ui-forward.log" 2>&1 &
  echo $! >"${state}/ui-forward.pid"
  local start now
  start="$(date +%s)"
  while true; do
    if curl --silent --fail --max-time 2 "http://127.0.0.1:${port}/nginx-health" >/dev/null 2>&1; then
      return 0
    fi
    now="$(date +%s)"
    if (( now - start >= 30 )); then
      echo "the UI did not answer on 127.0.0.1:${port}. See ${state}/ui-forward.log" >&2
      return 1
    fi
    sleep 1
  done
}

lab_wait_login() {
  local port="$1"
  local organization="$2"
  local password="$3"
  local jar="$4"
  local body start now code
  body="$(lab_login_body "${organization}" "${password}")"
  start="$(date +%s)"
  while true; do
    code="$(curl -sS -o "${jar}.body" -c "${jar}" -w '%{http_code}' \
      -H 'Content-Type: application/json' \
      --data "${body}" \
      "http://127.0.0.1:${port}/api/auth/login" || true)"
    if [[ "${code}" == "200" ]]; then
      return 0
    fi
    now="$(date +%s)"
    if (( now - start >= 120 )); then
      echo "sign-in returned HTTP ${code:-none}. The lab organization ${organization} did not get a session." >&2
      cat "${jar}.body" >&2 || true
      return 1
    fi
    sleep 2
  done
}

# Seeds the keys a developer gave the lab, through the same routes the console
# uses, so the lab's first Organization starts as a person's would after
# pasting them. With none given, nothing is called and the Chat shows its paste
# a key state. A rerun stores the given key again, over one pasted since. The
# body is built from the environment and goes to curl on standard input, so a
# key is on no command line.
#   LAB_ANTHROPIC_API_KEY, LAB_ANTHROPIC_BASE_URL (optional), LAB_CURSOR_API_KEY
lab_seed_connections() {
  local port="$1"
  local jar="$2"
  local body
  if [[ -n "${LAB_ANTHROPIC_API_KEY:-}" ]]; then
    body="$(python3 -c 'import json,os,sys
body = {"apiKey": os.environ["LAB_ANTHROPIC_API_KEY"]}
if os.environ.get("LAB_ANTHROPIC_BASE_URL"):
    body["baseUrl"] = os.environ["LAB_ANTHROPIC_BASE_URL"]
sys.stdout.write(json.dumps(body))')" || return 1
    lab_seed_connection "${port}" "${jar}" assistant "${body}" || return 1
  fi
  if [[ -n "${LAB_CURSOR_API_KEY:-}" ]]; then
    body="$(python3 -c 'import json,os,sys; sys.stdout.write(json.dumps({"apiKey": os.environ["LAB_CURSOR_API_KEY"]}))')" || return 1
    lab_seed_connection "${port}" "${jar}" cursor "${body}" || return 1
  fi
}

lab_seed_connection() {
  local port="$1"
  local jar="$2"
  local kind="$3"
  local body="$4"
  local code
  code="$(printf '%s' "${body}" | curl -sS --max-time 60 -o "${jar}.seed" -b "${jar}" -w '%{http_code}' \
    -X PUT -H 'Content-Type: application/json' --data @- \
    "http://127.0.0.1:${port}/api/connections/${kind}" || true)"
  if [[ "${code}" != "200" && "${code}" != "204" ]]; then
    echo "could not store the ${kind} key (HTTP ${code:-none})." >&2
    cat "${jar}.seed" >&2 || true
    return 1
  fi
  echo "stored the ${kind} key"
}

lab_issue_join() {
  local port="$1"
  local jar="$2"
  local name="$3"
  local dest="$4"
  local code
  code="$(curl -sS -o "${dest}" -b "${jar}" -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    --data "{\"name\":\"${name}\"}" \
    "http://127.0.0.1:${port}/api/connect/tokens" || true)"
  if [[ "${code}" != "201" ]]; then
    echo "could not mint a join token (HTTP ${code:-none})." >&2
    cat "${dest}" >&2 || true
    return 1
  fi
}

lab_json_field() {
  local json="$1"
  local field="$2"
  printf '%s' "${json}" | python3 -c '
import json, sys
data = json.load(sys.stdin)
value = data.get(sys.argv[1], "")
sys.stdout.write("" if value is None else str(value))
' "${field}"
}

lab_fetch_environments() {
  local port="$1"
  local jar="$2"
  curl -sS -b "${jar}" "http://127.0.0.1:${port}/api/environments"
}

lab_kustomization_ready() {
  local context="$1"
  local name="$2"
  local ready
  ready="$(kubectl --context "${context}" -n flux-system get "kustomization/${name}" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  [[ "${ready}" == "True" ]]
}

lab_wait_connected() {
  local port="$1"
  local jar="$2"
  local name="$3"
  local start now json status
  start="$(date +%s)"
  while true; do
    json="$(lab_fetch_environments "${port}" "${jar}" || true)"
    status="$(lab_environment_fresh "${json}" "${name}" "$(date +%s)")"
    if [[ "${status}" == "connected" ]]; then
      echo "environment ${name} is connected"
      return 0
    fi
    now="$(date +%s)"
    if (( now - start >= 300 )); then
      echo "environment ${name} did not show as connected. Last status: ${status:-absent}." >&2
      return 1
    fi
    sleep 2
  done
}

lab_wait_kustomization() {
  local context="$1"
  local name="$2"
  local seconds="$3"
  local start now
  start="$(date +%s)"
  while true; do
    if kubectl --context "${context}" -n flux-system get "kustomization/${name}" >/dev/null 2>&1; then
      kubectl --context "${context}" -n flux-system wait "kustomization/${name}" \
        --for=condition=Ready --timeout="${seconds}s"
      return
    fi
    now="$(date +%s)"
    if (( now - start >= seconds )); then
      echo "Flux Kustomization ${name} did not appear on ${context}" >&2
      return 1
    fi
    sleep 2
  done
}

lab_spoke_healthy() {
  local port="$1"
  local jar="$2"
  local name="$3"
  local context="$4"
  local arch="$5"
  local json status
  json="$(lab_fetch_environments "${port}" "${jar}" || true)"
  status="$(lab_environment_status "${json}" "${name}")"
  [[ "${status}" == "connected" ]] || return 1
  lab_kustomization_ready "${context}" instances || return 1
  if lab_wants_platform_kustomization "${arch}"; then
    lab_kustomization_ready "${context}" platform || return 1
  fi
}

# The Connect command keeps the public URL, which pods resolve through CoreDNS.
# The process that runs it is on the laptop, where that name does not resolve.
# A curl ahead of PATH sends only that process to the port-forward.
lab_write_curl_wrapper() {
  local dest="$1"
  local real="$2"
  shift 2
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf 'exec %s' "$(lab_shell_quote "${real}")"
    local arg
    for arg in "$@"; do
      printf ' %s' "$(lab_shell_quote "${arg}")"
    done
    printf ' %s\n' '"$@"'
  } >"${dest}"
  chmod +x "${dest}"
}

lab_join_curl_dir() {
  local dir="$1"
  local host="$2"
  local ip="$3"
  local ui_port="$4"
  local git_port="$5"
  local real
  real="$(command -v curl)"
  [[ -n "${real}" ]] || return 1
  mkdir -p "${dir}"
  lab_write_curl_wrapper "${dir}/curl" "${real}" \
    --resolve "${host}:${ui_port}:${ip}" \
    --resolve "${host}:${git_port}:${ip}"
}

lab_run_join() {
  local command="$1"
  local spoke="$2"
  local chart="$3"
  local state="$4"
  local host="${5:-${LAB_HOST:-host.docker.internal}}"
  local ui_port="${6:-${UI_PORT:-18080}}"
  local git_port="${7:-${GITEA_PORT:-3000}}"
  local kube="${state}/lab-spoke.kubeconfig"
  local bindir="${state}/join-bin"
  kind export kubeconfig --name "${spoke}" --kubeconfig "${kube}"
  lab_join_curl_dir "${bindir}" "${host}" "127.0.0.1" "${ui_port}" "${git_port}"
  echo "joining ${spoke} through Connect with --local"
  # PDS_CHART selects this checkout. The script still enrolls with the token
  # and installs the platform itself. pipefail makes a failed download fail
  # the pipeline instead of looking like an empty script succeeded.
  # PATH puts the wrapper first so enrollment curls, not only the download, hit 127.0.0.1.
  PATH="${bindir}:${PATH}" KUBECONFIG="${kube}" PDS_CHART="${chart}" bash -c "set -o pipefail; ${command}"
}

lab_build_images() {
  local root="$1"
  local hub="$2"
  local spoke="$3"
  if [[ "${BUILD}" == true ]]; then
    docker image inspect "${ADAPTER_IMAGE}:${TAG}" >/dev/null 2>&1 || true
    docker build --provenance=false -f "${root}/adapter/Dockerfile" -t "${ADAPTER_IMAGE}:${TAG}" "${root}"
    (cd "${root}" && npm -w @pds/ui run build)
    docker build --provenance=false -t "${UI_IMAGE}:${TAG}" "${root}/apps/ui"
  else
    docker image inspect "${ADAPTER_IMAGE}:${TAG}" >/dev/null 2>&1 || {
      echo "no local image ${ADAPTER_IMAGE}:${TAG}. Rerun without --no-build." >&2
      return 1
    }
    docker image inspect "${UI_IMAGE}:${TAG}" >/dev/null 2>&1 || {
      echo "no local image ${UI_IMAGE}:${TAG}. Rerun without --no-build." >&2
      return 1
    }
  fi
  kind load docker-image "${ADAPTER_IMAGE}:${TAG}" --name "${hub}"
  kind load docker-image "${UI_IMAGE}:${TAG}" --name "${hub}"
  kind load docker-image "${ADAPTER_IMAGE}:${TAG}" --name "${spoke}"
}

# The PXC operator keeps a MySQL database initializing until its load balancer
# Service has an address, and the PostgreSQL provider publishes no connection
# host until PgBouncer's has one. kind has none; this controller assigns addresses from
# the kind network. One controller serves every kind cluster on the host.
lab_ensure_load_balancer() {
  local name image
  name="${CLOUD_PROVIDER_NAME:-cloud-provider-kind}"
  image="${CLOUD_PROVIDER_IMAGE:-registry.k8s.io/cloud-provider-kind/cloud-controller-manager:v0.12.0}"
  if [[ -n "$(docker ps -q --filter "name=^${name}$")" ]]; then
    return 0
  fi
  docker rm -f "${name}" >/dev/null 2>&1 || true
  echo "starting ${name}"
  docker run -d --name "${name}" --restart unless-stopped --network kind \
    -v /var/run/docker.sock:/var/run/docker.sock "${image}" >/dev/null
}

# The controller stays while any kind cluster is left, including one this lab
# did not create.
lab_stop_load_balancer() {
  local name
  name="${CLOUD_PROVIDER_NAME:-cloud-provider-kind}"
  if [[ -z "$(kind get clusters 2>/dev/null)" ]]; then
    docker rm -f "${name}" >/dev/null 2>&1 || true
  fi
}

lab_down() {
  local root state name
  root="$(lab_root)"
  state="${STATE_DIR:-${root}/.local}"
  command -v docker >/dev/null 2>&1 || {
    echo "missing required tool: docker" >&2
    return 1
  }
  command -v kind >/dev/null 2>&1 || {
    echo "missing required tool: kind" >&2
    return 1
  }
  lab_stop_helpers "${state}"
  for name in "${HUB_CLUSTER:-hub}" "${SPOKE_CLUSTER:-spoke-a}"; do
    if lab_kind_exists "${name}"; then
      echo "deleting kind cluster ${name}"
      kind delete cluster --name "${name}"
    fi
  done
  lab_stop_load_balancer
  # The git sync and the kind mount write some files as root. A plain rm
  # then fails after the clusters are already gone.
  if [[ -d "${state}/lab-git" ]]; then
    chmod -R u+w "${state}/lab-git" 2>/dev/null || true
    rm -rf "${state}/lab-git" 2>/dev/null || sudo rm -rf "${state}/lab-git"
  fi
  rm -f "${state}/lab-spoke.kubeconfig"
  echo "lab removed: kind clusters, Gitea, and port-forwards"
}

lab_up() {
  local root state git_dir src host_name public_url git_url
  local hub_context spoke_context arch note wait_flag jar token_file
  local api_command join_command
  root="$(lab_root)"
  state="${STATE_DIR:-${root}/.local}"
  HUB_CLUSTER="${HUB_CLUSTER:-hub}"
  SPOKE_CLUSTER="${SPOKE_CLUSTER:-spoke-a}"
  HUB_NS="${HUB_NS:-everest-system}"
  UI_PORT="${UI_PORT:-18080}"
  GITEA_PORT="${GITEA_PORT:-3000}"
  GITEA_CONTAINER="${GITEA_CONTAINER:-lab-gitea}"
  GITEA_USER="${GITEA_USER:-hub}"
  GITEA_PASSWORD="${GITEA_PASSWORD:-lab-gitea}"
  GITEA_REPO="${GITEA_REPO:-pds}"
  K8S_IMAGE="${K8S_IMAGE:-kindest/node:v1.32.2}"
  TAG="${TAG:-dev}"
  ADAPTER_IMAGE="${ADAPTER_IMAGE:-ghcr.io/percona/percona-services-adapter}"
  UI_IMAGE="${UI_IMAGE:-ghcr.io/percona/percona-services-ui}"
  CHART_DIR="${CHART_DIR:-${root}/charts/pds}"
  BOOTSTRAP_ORGANIZATION="${BOOTSTRAP_ORGANIZATION:-dev}"
  BOOTSTRAP_PASSWORD="${BOOTSTRAP_PASSWORD:-percona-dev-login}"
  git_dir="${state}/lab-git"
  src="${root}/lab/multi-cluster"
  host_name="${LAB_HOST:-host.docker.internal}"
  hub_context="kind-${HUB_CLUSTER}"
  spoke_context="kind-${SPOKE_CLUSTER}"
  arch="$(uname -m)"

  lab_require_tools "${BUILD}" || return 1
  docker info >/dev/null 2>&1 || {
    echo "docker daemon is not reachable" >&2
    return 1
  }
  lab_require_memory "$(docker info --format '{{.MemTotal}}' 2>/dev/null || true)" || return 1

  note="$(lab_arm64_note "${arch}")"
  if [[ -n "${note}" ]]; then
    echo "${note}"
  fi

  mkdir -p "${state}"
  # A cluster left over from something else can share the lab's name and still
  # not mount this git directory. Fail before creating Gitea or installing.
  if lab_kind_exists "${HUB_CLUSTER}"; then
    lab_require_git_mount "${HUB_CLUSTER}" "${git_dir}" || return 1
  fi
  # Drop listeners this lab owns before the port check, so a rerun can bind
  # the same ports. A foreign listener still fails the check.
  lab_stop_helpers "${state}"
  lab_check_ports "UI_PORT:${UI_PORT}" "GITEA_PORT:${GITEA_PORT}" || return 1

  printf '\n== %s\n' "helm dependencies"
  "${root}/scripts/helm-deps.sh"

  printf '\n== %s\n' "git working tree"
  lab_prepare_git "${git_dir}" "${src}"

  public_url="$(lab_public_origin "${host_name}" "${UI_PORT}")"
  git_url="$(lab_git_clone_url "${host_name}" "${GITEA_PORT}" "${GITEA_USER}" "${GITEA_REPO}")"

  printf '\n== %s\n' "gitea"
  lab_start_gitea "${state}" "${git_dir}" "${GITEA_PORT}" "${host_name}"
  lab_start_git_sync "${git_dir}" "${state}/lab-git-commit.log" "${state}/lab-git-commit.pid"

  printf '\n== %s\n' "kind clusters"
  lab_create_kind "${HUB_CLUSTER}" "    extraMounts:
      - hostPath: ${git_dir}
        containerPath: /var/lab-git"
  # LAB_SPOKE_WORKERS is unset for a laptop. The hub smoke sets it so three
  # database members can schedule: with workers, the control plane takes none.
  lab_create_kind "${SPOKE_CLUSTER}" "" "${LAB_SPOKE_WORKERS:-0}"
  lab_ensure_load_balancer
  lab_ensure_host_alias "${HUB_CLUSTER}"
  lab_ensure_host_alias "${SPOKE_CLUSTER}"

  printf '\n== %s\n' "images"
  lab_build_images "${root}" "${HUB_CLUSTER}" "${SPOKE_CLUSTER}"

  printf '\n== %s\n' "helm hub"
  lab_wait_namespace "${hub_context}" "${HUB_NS}"
  wait_flag="$(helm_install_wait_flag)"
  HELM_SETS=()
  lab_fill_hub_helm_sets "${public_url}" "${git_url}" "${wait_flag}" || return 1
  helm upgrade --install pds "${CHART_DIR}" \
    --kube-context "${hub_context}" \
    "${HELM_SETS[@]}"
  kubectl --context "${hub_context}" -n "${HUB_NS}" rollout status deploy/pds-adapter --timeout=10m
  kubectl --context "${hub_context}" -n "${HUB_NS}" rollout status deploy/pds-ui --timeout=10m

  printf '\n== %s\n' "UI port-forward on 127.0.0.1:${UI_PORT}"
  lab_start_ui_forward "${hub_context}" "${HUB_NS}" "${UI_PORT}" "${state}"

  jar="${state}/lab-session"
  rm -f "${jar}" "${jar}.body" "${jar}.seed"
  lab_wait_login "${UI_PORT}" "${BOOTSTRAP_ORGANIZATION}" "${BOOTSTRAP_PASSWORD}" "${jar}"
  lab_seed_connections "${UI_PORT}" "${jar}"

  printf '\n== %s\n' "connect ${SPOKE_CLUSTER}"
  if lab_spoke_healthy "${UI_PORT}" "${jar}" "${SPOKE_CLUSTER}" "${spoke_context}" "${arch}"; then
    echo "environment ${SPOKE_CLUSTER} is already connected; leaving its agent in place"
  else
    token_file="${state}/lab-join.json"
    lab_issue_join "${UI_PORT}" "${jar}" "${SPOKE_CLUSTER}" "${token_file}"
    api_command="$(lab_json_field "$(cat "${token_file}")" command)"
    join_command="$(lab_connect_command "${api_command}" "${public_url}" "$(lab_json_field "$(cat "${token_file}")" token)")"
    if [[ "${LAB_CONNECT:-join}" == flux ]]; then
      # Flux is installed by the bootstrap, so the namespace is not waited for.
      flux_environment_connect "${SPOKE_CLUSTER}" "${state}" "${git_dir}" "${CHART_DIR}" \
        "$(lab_json_field "$(cat "${token_file}")" token)" "${public_url}" "${UI_PORT}"
    else
      lab_wait_namespace "${spoke_context}" "${HUB_NS}"
      lab_wait_namespace "${spoke_context}" flux-system
      lab_run_join "${join_command}" "${SPOKE_CLUSTER}" "${CHART_DIR}" "${state}"
    fi
  fi
  if [[ "${LAB_CONNECT:-join}" == flux ]]; then
    flux_environment_wait "${spoke_context}"
  fi
  lab_wait_connected "${UI_PORT}" "${jar}" "${SPOKE_CLUSTER}"
  if [[ "${LAB_CONNECT:-join}" != flux ]]; then
    lab_wait_kustomization "${spoke_context}" instances 600
    if lab_wants_platform_kustomization "${arch}"; then
      lab_wait_kustomization "${spoke_context}" platform 900
    fi
  fi

  cat <<EOF

Percona Database Service lab is running.

$(lab_signin_lines "${BOOTSTRAP_ORGANIZATION}" "${BOOTSTRAP_PASSWORD}" "${UI_PORT}")
  Hub cluster:        ${hub_context}
  Connected environment:  ${spoke_context} (Connect --local)
  Gitea:              http://127.0.0.1:${GITEA_PORT}  (${GITEA_USER} / ${GITEA_PASSWORD})
  Git clone:          ${git_url}

The Gitea password is throwaway too. It only exists so this laptop can serve Git.
${note}
Tear the lab down: scripts/up.sh --down
EOF
}
