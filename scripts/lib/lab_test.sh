#!/usr/bin/env bash
#
# Unit tests for the lab decisions in scripts/lib/lab.sh.
# Bring-up itself is not executed: these cover argument parsing, the memory
# floor, the port check, and the Connect command the lab runs.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=lab.sh
source "${REPO_ROOT}/scripts/lib/lab.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# The assignments are inputs to the sourced parser. This file does not read them.
# shellcheck disable=SC2034
reset_args() {
  BUILD=true
  DOWN=false
  INSECURE=false
  HUB=false
  KUBE_URL=""
  KUBE_CONTEXT=""
  KUBECONFIG_FILE=""
  INGRESS_HOST=""
  INGRESS_CLASS="nginx"
  HUB_PUBLIC_URL=""
  HUB_GITHUB_ORG=""
  HUB_GITHUB_SECRET=""
  HUB_GIT_URL=""
  HUB_CLUSTER_REGION=""
  REMOTE=false
}

parse() {
  reset_args
  up_parse_args "$@"
  up_mark_remote
}

# --- argument parsing ---

parse
[[ "$(up_mode)" == "lab" ]] || fail "no args should be the lab, got $(up_mode)"
[[ "${DOWN}" == false && "${BUILD}" == true && "${HUB}" == false && "${REMOTE}" == false ]] || fail "lab defaults"
pass "no args is the lab"

parse --down
[[ "$(up_mode)" == "lab" && "${DOWN}" == true ]] || fail "--down without a kubeconfig should tear the lab down"
pass "down is the lab"

parse --no-build
[[ "${BUILD}" == false && "$(up_mode)" == "lab" ]] || fail "--no-build should stay on the lab"
pass "no-build stays on the lab"

parse --kubeconfig /tmp/demo-kubeconfig
[[ "$(up_mode)" == "remote" && "${HUB}" == false && "${KUBECONFIG_FILE}" == /tmp/demo-kubeconfig ]] || fail "kubeconfig without --hub should parse as remote before validation"
rc=0
out="$(up_validate 2>&1)" || rc=$?
[[ "${rc}" -eq 2 && "${out}" == *"needs --hub"* ]] || fail "kubeconfig without --hub: ${out}"
pass "kubeconfig without hub is rejected"

parse --kubeconfig /tmp/demo-kubeconfig --hub --hub-public-url https://hub.example.com --hub-region us-east-1
[[ "$(up_mode)" == "hub" && "${HUB}" == true && "${REMOTE}" == true ]] || fail "kubeconfig --hub should stay hub mode"
[[ "${HUB_PUBLIC_URL}" == "https://hub.example.com" && "${HUB_CLUSTER_REGION}" == "us-east-1" ]] || fail "hub flags were dropped"
up_validate || fail "kubeconfig --hub should validate"
pass "kubeconfig --hub is unchanged"

parse --kube-context demo
rc=0
out="$(up_validate 2>&1)" || rc=$?
[[ "${rc}" -eq 2 && "${out}" == *"needs --hub"* ]] || fail "kube-context without --hub: ${out}"
pass "kube-context without hub is rejected"

parse --kubeconfig /tmp/demo-kubeconfig --kube-context demo --hub --insecure
up_validate || fail "hub kube context should validate"
[[ "${KUBE_CONTEXT}" == "demo" && "${INSECURE}" == true ]] || fail "hub cluster flags were dropped"
pass "hub keeps kube-context and insecure"

rc=0
out="$(reset_args; up_parse_args --hub 2>&1)" || rc=$?
[[ "${rc}" -eq 0 ]] || fail "--hub alone should parse; validation rejects it"
reset_args
up_parse_args --hub
up_mark_remote
rc=0
out="$(up_validate 2>&1)" || rc=$?
[[ "${rc}" -eq 2 ]] || fail "--hub without a cluster should fail, rc=${rc}"
[[ "${out}" == *"Pass --kubeconfig"* ]] || fail "--hub message: ${out}"
pass "hub requires a kubeconfig"

reset_args
up_parse_args --hub --kube-context demo
up_mark_remote
rc=0
out="$(up_validate 2>&1)" || rc=$?
[[ "${rc}" -eq 2 && "${out}" == *"Pass --kubeconfig"* ]] || fail "--hub --kube-context without kubeconfig: ${out}"
pass "hub context still needs a kubeconfig"

reset_args
up_parse_args --hub-git-url http://git.example/pds.git
up_mark_remote
rc=0
out="$(up_validate 2>&1)" || rc=$?
[[ "${rc}" -eq 2 && "${out}" == *"need --hub"* ]] || fail "hub flag without --hub: ${out}"
pass "hub settings need --hub"

rc=0
out="$(reset_args; up_parse_args --kubeconfig /tmp/demo-kubeconfig --hub --backup s3 2>&1)" || rc=$?
[[ "${rc}" -eq 2 && "${out}" == *"usage:"* ]] || fail "--backup should be rejected: ${out}"
pass "backup flag is gone"

parse --ingress-host ui.example.com
rc=0
out="$(up_validate 2>&1)" || rc=$?
[[ "${rc}" -eq 2 ]] || fail "lab ingress should fail"
pass "lab rejects ingress"

parse --kubeconfig /tmp/demo-kubeconfig --hub --ingress-host ui.example.com --ingress-class nginx
up_validate || fail "hub ingress flags should validate"
[[ "${INGRESS_HOST}" == "ui.example.com" && "${INGRESS_CLASS}" == "nginx" ]] || fail "hub ingress flags dropped"
pass "hub ingress flags still validate"

rc=0
out="$(reset_args; up_parse_args --not-a-flag 2>&1)" || rc=$?
[[ "${rc}" -eq 2 ]] || fail "unknown flag should exit 2, rc=${rc}"
[[ "${out}" == *"usage:"* ]] || fail "usage text missing: ${out}"
pass "unknown flag"

rc=0
out="$(reset_args; up_parse_args --help 2>&1)" || rc=$?
[[ "${rc}" -eq 2 && "${out}" == *"usage:"* ]] || fail "help should print usage"
[[ "${out}" == *"lab"* ]] || fail "usage should describe the lab"
pass "usage describes the lab"

# --- memory ---

min="$(lab_min_memory_bytes)"
[[ "${min}" =~ ^[0-9]+$ ]] || fail "memory floor is not a byte count: ${min}"
# The floor sits under a full 8 GiB because Docker reports less than the
# Desktop slider, and at or above 8 GiB so a 4 GB machine is refused.
[[ "${min}" -ge $((7 * 1024 * 1024 * 1024)) && "${min}" -le $((8 * 1024 * 1024 * 1024)) ]] || fail "floor ${min} is not about 8 GB"
pass "memory floor is about 8 GB"

lab_require_memory "${min}" || fail "the floor itself should pass"
lab_require_memory $((min + 1)) || fail "above the floor should pass"
rc=0
out="$(lab_require_memory $((min - 1)) 2>&1)" || rc=$?
[[ "${rc}" -ne 0 ]] || fail "one byte under the floor should be refused"
[[ "${out}" == *"about 8 GB"* ]] || fail "memory message: ${out}"
rc=0
out="$(lab_require_memory $((4 * 1024 * 1024 * 1024)) 2>&1)" || rc=$?
[[ "${rc}" -ne 0 && "${out}" == *"about 8 GB"* ]] || fail "4 GB should be refused: ${out}"
rc=0
out="$(lab_require_memory "nope" 2>&1)" || rc=$?
[[ "${rc}" -ne 0 && "${out}" == *"could not read Docker memory"* ]] || fail "bad memory: ${out}"
pass "memory check"

# --- ports ---

unset -f lab_port_open_hook lab_port_owned_hook 2>/dev/null || true
lab_port_open_hook() { return 1; }
lab_check_ports "UI_PORT:18080" "GITEA_PORT:3000" || fail "free ports should pass"
pass "free ports"

lab_port_open_hook() {
  [[ "$1" == "3000" ]]
}
rc=0
out="$(lab_check_ports "UI_PORT:18080" "GITEA_PORT:3000" 2>&1)" || rc=$?
[[ "${rc}" -ne 0 ]] || fail "a busy port should fail"
[[ "${out}" == *"3000"* && "${out}" == *"GITEA_PORT"* ]] || fail "port message: ${out}"
pass "busy port"

lab_port_owned_hook() {
  [[ "$1" == "GITEA_PORT" ]]
}
lab_check_ports "UI_PORT:18080" "GITEA_PORT:3000" || fail "our own listener should not block a rerun"
pass "owned port"
unset -f lab_port_open_hook lab_port_owned_hook

# --- join command ---

cmd="$(lab_join_command "http://host.docker.internal:18080/" "jnt_labtoken")"
[[ "${cmd}" == *"http://host.docker.internal:18080/api/internal/connect/bootstrap.sh"* ]] || fail "join url: ${cmd}"
[[ "${cmd}" == *"--token"* && "${cmd}" == *"--local"* ]] || fail "join flags: ${cmd}"
[[ "${cmd}" != *"HUB_AGENT"* && "${cmd}" != *"agentToken"* ]] || fail "join used a shared agent token: ${cmd}"
pass "join command"

quoted="$(lab_shell_quote "tok'en")"
[[ "${quoted}" == "'tok'\\''en'" ]] || fail "shell quote: ${quoted}"
pass "shell quote"

api="curl -fsSL 'http://host.docker.internal:18080/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_abc'"
local_cmd="$(lab_join_command_local "${api}")"
[[ "${local_cmd}" == "${api} --local" ]] || fail "append --local: ${local_cmd}"
again="$(lab_join_command_local "${local_cmd}")"
[[ "${again}" == "${local_cmd}" ]] || fail "--local doubled: ${again}"
pass "local flag once"

rc=0
out="$(lab_join_command_local "" 2>&1)" || rc=$?
[[ "${rc}" -ne 0 ]] || fail "an empty Connect command should fail"
pass "empty connect command"

# An update takes --token and nothing else, so it never gets --local.
update_cmd="$(lab_update_command "${api}")"
[[ "${update_cmd}" == "curl -fsSL 'http://host.docker.internal:18080/api/internal/connect/bootstrap.sh' | bash -s -- --update --token 'jnt_abc'" ]] \
  || fail "update command: ${update_cmd}"
[[ "${update_cmd}" != *"--local"* ]] || fail "update command carries --local: ${update_cmd}"
rc=0
out="$(lab_update_command "" 2>&1)" || rc=$?
[[ "${rc}" -ne 0 ]] || fail "an empty Connect command should fail the update"
rc=0
out="$(lab_update_command "echo no token here" 2>&1)" || rc=$?
[[ "${rc}" -ne 0 ]] || fail "a command with no --token should fail the update"
pass "update command"

picked="$(lab_connect_command "${api}" "http://ignored" "ignored")"
[[ "${picked}" == "${api} --local" ]] || fail "api command should win: ${picked}"
picked="$(lab_connect_command "" "http://host.docker.internal:18080" "jnt_fallback")"
[[ "${picked}" == *"--token 'jnt_fallback' --local" ]] || fail "fallback command: ${picked}"
pass "connect command selection"

# --- organization, addresses, helm sets ---

lab_valid_organization dev || fail "dev should be a named organization"
if lab_valid_organization "*"; then
  fail "a star organization must not be seeded"
fi
BOOTSTRAP_ORGANIZATION="*"
BOOTSTRAP_PASSWORD="percona-dev-login"
rc=0
out="$(lab_check_bootstrap 2>&1)" || rc=$?
[[ "${rc}" -ne 0 && "${out}" == *"named"* ]] || fail "star organization: ${out}"
BOOTSTRAP_ORGANIZATION="dev"
BOOTSTRAP_PASSWORD="short"
rc=0
out="$(lab_check_bootstrap 2>&1)" || rc=$?
[[ "${rc}" -ne 0 ]] || fail "a short password should fail"
BOOTSTRAP_PASSWORD="percona-dev-login"
lab_check_bootstrap || fail "dev organization should pass"
pass "named organization"

origin="$(lab_public_origin host.docker.internal 18080)"
[[ "${origin}" == "http://host.docker.internal:18080" ]] || fail "public origin: ${origin}"
git_url="$(lab_git_clone_url host.docker.internal 3000 hub pds)"
[[ "${git_url}" == "http://host.docker.internal:3000/hub/pds.git" ]] || fail "git url: ${git_url}"
[[ "${git_url}" != *"@"* ]] || fail "git url must not carry credentials"
pass "lab addresses"

note="$(lab_arm64_note arm64)"
[[ "${note}" == *"without monitoring"* ]] || fail "arm64 note: ${note}"
[[ -z "$(lab_arm64_note amd64)" ]] || fail "amd64 should not warn"
lab_wants_platform_kustomization arm64 && fail "arm64 must not wait for the platform kustomization"
lab_wants_platform_kustomization aarch64 && fail "aarch64 must not wait for the platform kustomization"
lab_wants_platform_kustomization amd64 || fail "amd64 should wait for platform"
pass "arm64 skips monitoring"

# shellcheck disable=SC2034 # inputs to lab_fill_hub_helm_sets
{
  CHART_DIR="${REPO_ROOT}/charts/pds"
  ADAPTER_IMAGE="ghcr.io/percona/percona-services-adapter"
  UI_IMAGE="ghcr.io/percona/percona-services-ui"
  TAG="dev"
  BOOTSTRAP_ORGANIZATION="dev"
  BOOTSTRAP_PASSWORD="percona-dev-login"
  UI_PORT="18080"
}
HELM_SETS=()
lab_fill_hub_helm_sets "http://host.docker.internal:18080" "http://host.docker.internal:3000/hub/pds.git" "--wait" || fail "helm sets"
joined="$(printf '%s\n' "${HELM_SETS[@]}")"
[[ "${joined}" == *"values-hub-lab.yaml"* && "${joined}" != *"values-hub.yaml"* ]] || fail "hub value files: ${joined}"
[[ "${joined}" == *"bootstrapOrganization.enabled=true"* ]] || fail "bootstrap organization off"
[[ "${joined}" == *"adapter.state.organization=dev"* ]] || fail "organization was not pinned"
[[ "${joined}" != *"agentToken"* && "${joined}" != *"agentEnvironments"* && "${joined}" != *"values-spoke.yaml"* ]] || fail "spoke install leaked into the hub: ${joined}"
[[ "${joined}" == *"host.docker.internal:18080"* && "${joined}" == *"host.docker.internal:3000/hub/pds.git"* ]] || fail "lab urls missing"
[[ "${joined}" == *"adapter.hub.oauthIssuer=http://127.0.0.1:18080"* ]] || fail "the issuer must be the loopback address of the port-forward: ${joined}"
pass "hub helm sets"

# --- pure helpers the waits use ---

[[ "$(lab_namespace_action "")" == "ok" ]] || fail "missing namespace"
[[ "$(lab_namespace_action Active)" == "ok" ]] || fail "active namespace"
[[ "$(lab_namespace_action Terminating)" == "wait" ]] || fail "terminating namespace"
[[ "$(lab_namespace_action Failed)" == "fail" ]] || fail "other phase"
pass "namespace phase"

status="$(lab_environment_status '{"hub":true,"environments":[{"name":"spoke-a","status":"connected"}]}' spoke-a)"
[[ "${status}" == "connected" ]] || fail "environment status: ${status}"
[[ -z "$(lab_environment_status '{"environments":[]}' spoke-a)" ]] || fail "missing environment should be empty"
pass "environment status"

body="$(lab_login_body dev 'percona-dev-login')"
[[ "${body}" == '{"username": "dev", "password": "percona-dev-login"}' || "${body}" == '{"username":"dev","password":"percona-dev-login"}' ]] || fail "login body: ${body}"
pass "login body"

corefile="$(printf '%s\n' '.:53 {' '    forward . /etc/resolv.conf' '}')"
patched="$(lab_coredns_with_host "${corefile}" 192.168.65.254 host.docker.internal)"
[[ "${patched}" == *"192.168.65.254 host.docker.internal"* ]] || fail "coredns patch: ${patched}"
again="$(lab_coredns_with_host "${patched}" 10.0.0.1 host.docker.internal)"
[[ "${again}" == *"10.0.0.1 host.docker.internal"* ]] || fail "coredns address not updated: ${again}"
[[ "${again}" != *"192.168.65.254"* ]] || fail "old coredns address left behind: ${again}"
[[ "$(printf '%s\n' "${again}" | grep -c 'hosts {')" -eq 1 ]] || fail "coredns hosts block duplicated: ${again}"
pass "coredns host alias"

ipv6_first=$'fdc4:f303:9324::254 host.docker.internal'
ahosts=$'192.168.65.254 STREAM host.docker.internal'
ip="$(lab_host_ip_from_answers "${ahosts}" "${ipv6_first}")" || fail "ipv4 answer should win"
[[ "${ip}" == "192.168.65.254" ]] || fail "picked ${ip} from an ipv6-first hosts file"
if lab_host_ip_from_answers '' "${ipv6_first}"; then
  fail "an ipv6-only answer must not be pinned"
fi
ip="$(lab_host_ip_from_answers '' $'fdc4:f303:9324::254 host.docker.internal\n192.168.65.254 host.docker.internal')" || fail "later ipv4 should be found"
[[ "${ip}" == "192.168.65.254" ]] || fail "skipped past ipv6: ${ip}"
pass "ipv4 host address"

# set -e stops on a failing assignment. A node with no address must leave
# the caller running so the Linux gateway fallback can still run.
if ! (
  set -euo pipefail
  docker() { return 1; }
  ip="$(lab_host_ip_from_node "missing")"
  [[ -z "${ip}" ]]
); then
  fail "a missing host address aborted the caller"
fi
pass "missing host address does not abort"

# A rerun finds files the kind mount wrote as root. chmod then returns
# non-zero, and set -e must not stop the lab before sudo can fix them.
# A fresh bash keeps set -e on; an if-subshell would turn it off.
if ! bash -c '
set -euo pipefail
# shellcheck source=lab.sh
source "$1"
git_dir="$2/root-owned-git"
sudo_log="$2/sudo-chmod"
mkdir -p "${git_dir}/.git"
chmod() { return 1; }
sudo() { printf "%s\n" "$*" >"${sudo_log}"; }
lab_prepare_git "${git_dir}" "$2/unused-src"
[[ "$(cat "${sudo_log}")" == "chmod -R a+rwX ${git_dir}" ]]
' _ "${REPO_ROOT}/scripts/lib/lab.sh" "${WORK}"; then
  fail "a root-owned git tree aborted the rerun"
fi
pass "root-owned git tree does not abort"

# A MySQL database stays initializing until its load balancer Service has an
# address. One controller serves every kind cluster, so a second start leaves
# it running and the last cluster to go takes it with it.
lb_log="${WORK}/lb-docker"
: >"${lb_log}"
(
  docker() {
    case "$1" in
      ps) printf 'cid\n' ;;
      *) printf '%s\n' "$*" >>"${lb_log}" ;;
    esac
  }
  lab_ensure_load_balancer
)
[[ ! -s "${lb_log}" ]] || fail "a running load balancer was started again: $(cat "${lb_log}")"
: >"${lb_log}"
(
  docker() {
    case "$1" in
      ps) return 0 ;;
      *) printf '%s\n' "$*" >>"${lb_log}" ;;
    esac
  }
  lab_ensure_load_balancer
)
[[ "$(cat "${lb_log}")" == *"run -d --name cloud-provider-kind --restart unless-stopped --network kind"* ]] || fail "load balancer was not started: $(cat "${lb_log}")"
[[ "$(cat "${lb_log}")" == *"registry.k8s.io/cloud-provider-kind/cloud-controller-manager:v0.12.0"* ]] || fail "load balancer image: $(cat "${lb_log}")"
: >"${lb_log}"
(
  kind() { printf 'hub\n'; }
  docker() { printf '%s\n' "$*" >>"${lb_log}"; }
  lab_stop_load_balancer
)
[[ ! -s "${lb_log}" ]] || fail "load balancer removed while a cluster remained: $(cat "${lb_log}")"
: >"${lb_log}"
(
  kind() { return 0; }
  docker() { printf '%s\n' "$*" >>"${lb_log}"; }
  lab_stop_load_balancer
)
[[ "$(cat "${lb_log}")" == "rm -f cloud-provider-kind" ]] || fail "load balancer was not removed: $(cat "${lb_log}")"
pass "kind load balancer"

# A rerun restarts the hub. The cluster stays "connected" until the last
# beat is 90s old, then flips. The wait has to see a beat young enough
# that the next check still finds it connected.
now="$(date +%s)"
recent="$(python3 -c 'from datetime import datetime, timezone, timedelta; print((datetime.now(timezone.utc)-timedelta(seconds=10)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
old="$(python3 -c 'from datetime import datetime, timezone, timedelta; print((datetime.now(timezone.utc)-timedelta(seconds=80)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
fresh_json="$(printf '{"environments":[{"name":"spoke-a","status":"connected","lastSeen":"%s"}]}' "${recent}")"
stale_json="$(printf '{"environments":[{"name":"spoke-a","status":"connected","lastSeen":"%s"}]}' "${old}")"
[[ "$(lab_environment_fresh "${fresh_json}" spoke-a "${now}")" == "connected" ]] || fail "a recent beat was not fresh"
[[ "$(lab_environment_fresh "${stale_json}" spoke-a "${now}")" == "unreachable" ]] || fail "a beat about to expire counted as connected"
[[ "$(lab_environment_fresh '{"environments":[{"name":"spoke-a","status":"unreachable","lastSeen":"'"${recent}"'"}]}' spoke-a "${now}")" == "unreachable" ]] || fail "unreachable was rewritten"
pass "heartbeat freshness"

wrapper="$(mktemp -d "${WORK}/curl.XXXXXX")"
recorded="${WORK}/curl-args"
cat >"${wrapper}/real-curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >"${CURL_RECORD}"
EOF
chmod +x "${wrapper}/real-curl"
lab_write_curl_wrapper "${wrapper}/curl" "${wrapper}/real-curl" \
  --resolve host.docker.internal:18080:127.0.0.1 \
  --resolve host.docker.internal:3000:127.0.0.1
CURL_RECORD="${recorded}" "${wrapper}/curl" -fsSL 'http://host.docker.internal:18080/api/internal/connect/bootstrap.sh'
[[ "$(cat "${recorded}")" == *"--resolve"$'\n'"host.docker.internal:18080:127.0.0.1"* ]] || fail "join curl did not resolve to localhost: $(cat "${recorded}")"
[[ "$(cat "${recorded}")" == *"http://host.docker.internal:18080/api/internal/connect/bootstrap.sh"* ]] || fail "join url was rewritten: $(cat "${recorded}")"
pass "join curl stays on the public name"

listing=$'/Users/me/lab-git /var/lab-git\n/lib/modules /lib/modules'
lab_mount_matches "${listing}" /Users/me/lab-git /var/lab-git || fail "direct mount"
lab_mount_matches $'/host_mnt/Users/me/lab-git /var/lab-git' /Users/me/lab-git /var/lab-git || fail "docker desktop mount"
if lab_mount_matches $'/host_mnt/other /var/poc-git' /Users/me/lab-git /var/lab-git; then
  fail "a foreign mount matched"
fi
pass "git mount"

cfg="$(lab_kind_config hub "    extraMounts:
      - hostPath: /tmp/lab-git
        containerPath: /var/lab-git")"
[[ "${cfg}" == *"name: hub"* && "${cfg}" == *"/tmp/lab-git"* && "${cfg}" == *"/var/lab-git"* ]] || fail "kind config: ${cfg}"
if printf '%s\n' "${cfg}" | grep -q 'role: worker'; then
  fail "default kind config added a worker: ${cfg}"
fi
pass "kind config"

cfg="$(lab_kind_config spoke "" 2)"
workers="$(printf '%s\n' "${cfg}" | grep -c 'role: worker')"
planes="$(printf '%s\n' "${cfg}" | grep -c 'role: control-plane')"
[[ "${workers}" -eq 2 && "${planes}" -eq 1 ]] || fail "spoke workers: ${cfg}"
printf '%s\n' "${cfg}" | grep -q 'kindest/node' || fail "worker image: ${cfg}"
if lab_kind_config spoke "" no >/dev/null 2>&1; then
  fail "kind config accepted a bad worker count"
fi
pass "kind workers"

tools="$(lab_tool_list false)"
[[ "${tools}" == *"docker"* && "${tools}" == *"kind"* && "${tools}" == *"kubectl"* && "${tools}" == *"helm"* && "${tools}" == *"flux"* ]] || fail "tools: ${tools}"
[[ "${tools}" != *"node"* ]] || fail "node should not be required when not building"
tools="$(lab_tool_list true)"
[[ "${tools}" == *"node"* && "${tools}" == *"npm"* ]] || fail "build tools: ${tools}"
pass "tool list"

empty="$(mktemp -d "${WORK}/bin.XXXXXX")"
rc=0
out="$(PATH="${empty}" lab_require_tools false 2>&1)" || rc=$?
[[ "${rc}" -ne 0 && "${out}" == *"missing required tool: docker"* ]] || fail "missing tool: ${out}"
pass "missing tool message"

lines="$(lab_signin_lines dev percona-dev-login 18080)"
[[ "${lines}" == *"dev / percona-dev-login"* && "${lines}" == *"throwaway"* && "${lines}" == *"127.0.0.1:18080/app/"* ]] || fail "signin lines: ${lines}"
pass "signin lines"

# --- seeding a key through the route a person uses ---

fakebin="$(mktemp -d "${WORK}/fakebin.XXXXXX")"
cat >"${fakebin}/curl" <<'EOF'
#!/usr/bin/env bash
# Records each call: its arguments, then its standard input. The status code
# a call answers with comes from FAKE_CODE.
{
  printf 'ARGS %s\n' "$*"
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == "-o" ]]; then printf '%s' '{"message":"fake body"}' >"$2"; fi
    shift
  done
  printf 'STDIN %s\n' "$(cat)"
} >>"${CURL_LOG}"
printf '%s' "${FAKE_CODE:-200}"
EOF
chmod +x "${fakebin}/curl"

seed_log="${WORK}/seed-log"
# python3 builds the request body. This one records its arguments, then runs the
# real interpreter, so a key passed as an argument shows up in the log.
real_python="$(command -v python3)"
cat >"${fakebin}/python3" <<EOF
#!/usr/bin/env bash
printf 'PYARGS %s\\n' "\$*" >>"\${CURL_LOG}"
exec "${real_python}" "\$@"
EOF
chmod +x "${fakebin}/python3"

run_seed() {
  : >"${seed_log}"
  PATH="${fakebin}:${PATH}" CURL_LOG="${seed_log}" lab_seed_connections 18080 "${WORK}/jar"
}

(
  unset LAB_ANTHROPIC_API_KEY LAB_ANTHROPIC_BASE_URL LAB_CURSOR_API_KEY
  run_seed
  [[ ! -s "${seed_log}" ]] || fail "a lab given no key still called the API: $(cat "${seed_log}")"
)
pass "no key given, nothing seeded"

(
  unset LAB_CURSOR_API_KEY
  export LAB_ANTHROPIC_API_KEY="sk-ant-lab-secret" LAB_ANTHROPIC_BASE_URL="https://proxy.example"
  run_seed
  log="$(cat "${seed_log}")"
  [[ "${log}" == *"-X PUT"* && "${log}" == *"http://127.0.0.1:18080/api/connections/assistant"* ]] \
    || fail "the Anthropic key did not go to PUT /api/connections/assistant: ${log}"
  [[ "${log}" == *'STDIN {"apiKey": "sk-ant-lab-secret", "baseUrl": "https://proxy.example"}'* ]] \
    || fail "the Anthropic body is wrong: ${log}"
  # Every recorded line but the request body: a python3 program spans several.
  [[ "$(grep -v '^STDIN' "${seed_log}")" != *"sk-ant-lab-secret"* ]] \
    || fail "the key is on a command line, where ps shows it"
  [[ "${log}" == *PYARGS* ]] || fail "the body was not built by the recording python3, so the check above proves nothing"
  [[ "${log}" != *"/api/connections/cursor"* ]] || fail "an unset Cursor key was seeded"
)
pass "an Anthropic key is seeded through PUT /api/connections/assistant"

(
  unset LAB_ANTHROPIC_API_KEY LAB_ANTHROPIC_BASE_URL
  export LAB_CURSOR_API_KEY="cursor-lab-secret"
  run_seed
  log="$(cat "${seed_log}")"
  [[ "${log}" == *"http://127.0.0.1:18080/api/connections/cursor"* ]] || fail "the Cursor key did not go to PUT /api/connections/cursor: ${log}"
  [[ "${log}" == *'STDIN {"apiKey": "cursor-lab-secret"}'* ]] || fail "the Cursor body is wrong: ${log}"
)
pass "a Cursor key is seeded through PUT /api/connections/cursor"

(
  unset LAB_CURSOR_API_KEY LAB_ANTHROPIC_BASE_URL
  export LAB_ANTHROPIC_API_KEY="sk-ant-lab-secret" FAKE_CODE=400
  rc=0
  out="$(run_seed 2>&1)" || rc=$?
  [[ "${rc}" -ne 0 ]] || fail "a refused key did not fail the lab"
  [[ "${out}" == *"HTTP 400"* && "${out}" == *"assistant"* ]] || fail "the failure does not say what was refused: ${out}"
  [[ "${out}" != *"sk-ant-lab-secret"* ]] || fail "the failure printed the key"
)
pass "a refused key fails with the status and never prints the key"

# --- wiring: up.sh is the lab, and the old entry points are gone ---

rc=0
help="$( "${REPO_ROOT}/scripts/up.sh" --help 2>&1 )" || rc=$?
[[ "${rc}" -eq 2 ]] || fail "up.sh --help rc=${rc}"
[[ "${help}" == *"lab"* ]] || fail "up.sh usage does not describe the lab"
[[ "${help}" == *"--hub"* ]] || fail "up.sh usage dropped --hub"
[[ "${help}" != *"--backup"* && "${help}" != *"S3_BUCKET"* ]] || fail "up.sh usage still offers backup flags: ${help}"
pass "up.sh usage"

kube="$(mktemp)"
rc=0
out="$("${REPO_ROOT}/scripts/up.sh" --kubeconfig "${kube}" 2>&1)" || rc=$?
rm -f "${kube}"
[[ "${rc}" -eq 2 && "${out}" == *"needs --hub"* ]] || fail "up.sh kubeconfig without --hub: ${out}"
pass "up.sh rejects kubeconfig without hub"

grep -q 'lab_up' "${REPO_ROOT}/scripts/up.sh" || fail "up.sh does not bring up the lab"
pass "up.sh calls the lab"

[[ ! -e "${REPO_ROOT}/scripts/lab-multi-cluster.sh" ]] || fail "lab-multi-cluster.sh still exists"
[[ ! -e "${REPO_ROOT}/scripts/lab-git-http.py" ]] || fail "lab-git-http.py still exists"
pass "old lab entry points deleted"

if matches="$(grep -RIn --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=.local --exclude=lab_test.sh -e 'lab-multi-cluster' -e 'lab-git-http.py' "${REPO_ROOT}")"; then
  printf '%s\n' "${matches}" >&2
  fail "old lab names are still referenced"
fi
pass "old lab names are gone"
