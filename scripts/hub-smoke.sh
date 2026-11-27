#!/usr/bin/env bash
#
# Nightly hub lifecycle. Brings the lab up and drives the hub API.
# One environment, a kind cluster, runs the database. The hub commits Instances
# to Git and Flux applies them. A database the hub did not create is a
# work item for the agent in that environment.
#
#   scripts/hub-smoke.sh                       lab, then the MySQL lifecycle below
#   scripts/hub-smoke.sh --engine postgresql   lab, then the PostgreSQL lifecycle
#   scripts/hub-smoke.sh --lab-only            lab and sign in, no database: a
#                                              hub with a connected environment
#                                              for the MCP binary's contract
#                                              tests
#   scripts/hub-smoke.sh --flux                lab whose environment is connected the
#                                              way the control plane does it, with
#                                              Flux and a rendered HelmRelease and
#                                              no join script, then a database
#                                              created, Ready and deleted
#   scripts/hub-smoke.sh --dump                kubectl, adapter, agent, Flux, and
#                                              Gitea logs
#
# The lifecycle, each on its own line. Steps marked MySQL run for MySQL only:
#   create a Dev database of the engine and check the Instance the
#   environment applied
#   wait until the heartbeat says Ready
#   wait until monitoring has a connections reading, which only the engine's
#   own exporter reports
#   MySQL: make the database's MySQL unreachable for monitoring, wait until
#   the hub lists service_down firing for it, restore it, and wait until the
#   alert is gone
#   scale to 3 members, on the cluster and in the heartbeat, then back to 1
#   MySQL: keep an unmodeled field across a scale to 2 CPU and one member of
#   a database the hub only sees from heartbeats, then delete that database
#   set a backup schedule and read it back
#   take an on-demand backup and wait for it to succeed
#   restore that backup into a new database and wait for Ready
#   update the environment in place with the join script's --update on the
#   same pin, wait until it is Connected, and check both databases are
#   still Ready
#   delete what the run created and wait until the connected environment has
#   none of it
#
# The lifecycle below is the pin contract. The smoke workflow runs it as two
# suites, mysql and postgresql, a third, mcp, that brings the lab up with
# --lab-only and runs the MCP binary's contract tests against it, signed in as
# the organization the lab seeds, and a fourth, flux, that connects the
# environment through scripts/lib/flux-environment.sh. Each suite brings up its own lab. Dispatch the
# smoke workflow on the branch to run them. It does not gate a pull request.
#
# The lab stays up when this command exits. The workflow collects logs and then
# runs scripts/up.sh --down. On a laptop, tear the lab down the same way.
#
# Environment:
#   ENGINE                       mysql or postgresql (default mysql); --engine
#                                overrides it
#   HUB_CLUSTER, SPOKE_CLUSTER   kind cluster names (default hub, spoke-a); the
#                                connected environment takes its name
#   NAMESPACE                    database namespace (default pds)
#   UI_PORT                      hub UI (default 18080)
#   LAB_SPOKE_WORKERS            extra nodes on the connected environment
#                                (default 4, so three members can schedule)
#   LIFECYCLE_TIMEOUT            seconds for one wait (default 1200)
#   FAILED_GRACE                 seconds a new database may report failed
#                                before the wait for Ready gives up (default 300)
#   ALERT_TIMEOUT                seconds for monitoring to read the database,
#                                for the alert to fire, and again for it to
#                                clear (default 900; the rule holds 2m)
#   ALERT_MONITOR_USER           the MySQL account monitoring connects as
#                                (default monitor)
#   BOOTSTRAP_ORGANIZATION, BOOTSTRAP_PASSWORD
#                                the organization the lab seeds
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/lifecycle.sh
source "${REPO_ROOT}/scripts/lib/lifecycle.sh"
# shellcheck source=lib/lab.sh
source "${REPO_ROOT}/scripts/lib/lab.sh"
# shellcheck source=lib/bounded.sh
source "${REPO_ROOT}/scripts/lib/bounded.sh"

HUB_CLUSTER="${HUB_CLUSTER:-hub}"
SPOKE_CLUSTER="${SPOKE_CLUSTER:-spoke-a}"
NAMESPACE="${NAMESPACE:-pds}"
HUB_NS="${HUB_NS:-everest-system}"
UI_PORT="${UI_PORT:-18080}"
BOOTSTRAP_ORGANIZATION="${BOOTSTRAP_ORGANIZATION:-dev}"
BOOTSTRAP_PASSWORD="${BOOTSTRAP_PASSWORD:-percona-dev-login}"
GITEA_CONTAINER="${GITEA_CONTAINER:-lab-gitea}"
K8S_IMAGE="${K8S_IMAGE:-kindest/node:v1.32.2}"
GITEA_IMAGE="${GITEA_IMAGE:-gitea/gitea:1.22}"
LIFECYCLE_TIMEOUT="${LIFECYCLE_TIMEOUT:-1200}"
ALERT_TIMEOUT="${ALERT_TIMEOUT:-900}"
FAILED_GRACE="${FAILED_GRACE:-300}"
ALERT_MONITOR_USER="${ALERT_MONITOR_USER:-monitor}"
# Kind keeps the control plane tainted once it has workers, so only workers
# take databases. Three members need a node each, a hosted runner gives every
# node 2 CPU, and the fourth worker leaves room for the platform beside them.
LAB_SPOKE_WORKERS="${LAB_SPOKE_WORKERS:-4}"

ENGINE="${ENGINE:-mysql}"
DUMP=false
LAB_ONLY=false
FLUX=false
usage() {
  echo "usage: $(basename "$0") [--engine mysql|postgresql] [--lab-only | --flux] [--dump]" >&2
  exit 2
}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dump) DUMP=true ;;
    --lab-only) LAB_ONLY=true ;;
    --flux) FLUX=true ;;
    --engine)
      [[ $# -ge 2 ]] || usage
      ENGINE="$2"
      shift
      ;;
    *) usage ;;
  esac
  shift
done
[[ "${FLUX}" == true && "${LAB_ONLY}" == true ]] && usage
PROVIDER="$(lifecycle_provider "${ENGINE}")" || usage
PRESET="$(lifecycle_preset "${ENGINE}")"
ENGINE_LABEL="$(lifecycle_engine_label "${ENGINE}")"

current_step="hub smoke"
explained=false
dump_only=false
tmp=""
monitor_locked=false
db_name=""
api_body=""
api_cookies=""

spoke_kube() { kubectl --context "kind-${SPOKE_CLUSTER}" "$@"; }
hub_kube() { kubectl --context "kind-${HUB_CLUSTER}" "$@"; }

step() {
  current_step="$1"
  printf '\n== %s\n' "$1"
}

# One dotted path, such as backupSchedule.period. Numbers print without a
# trailing fraction when they are whole. Missing and null are failures.
json_field() {
  local json="$1" path="$2"
  printf '%s' "${json}" | python3 -c 'import json,sys
data=json.loads(sys.stdin.read())
cur=data
for part in sys.argv[1].split("."):
    if not isinstance(cur, dict) or part not in cur or cur[part] is None:
        sys.exit(1)
    cur=cur[part]
if isinstance(cur, bool):
    print("true" if cur else "false")
elif isinstance(cur, float) and cur.is_integer():
    print(int(cur))
elif isinstance(cur, (int, str)):
    print(cur)
else:
    sys.exit(1)
' "${path}"
}

api_json() { cat "${api_body}"; }

api() {
  local method="$1" path="$2" body="${3:-}"
  local url="http://127.0.0.1:${UI_PORT}${path}"
  if [[ -n "${body}" ]]; then
    curl --silent --output "${api_body}" --write-out '%{http_code}' --max-time 30 \
      --cookie "${api_cookies}" \
      -X "${method}" -H 'Content-Type: application/json' -d "${body}" \
      "${url}"
  else
    curl --silent --output "${api_body}" --write-out '%{http_code}' --max-time 30 \
      --cookie "${api_cookies}" \
      -X "${method}" "${url}"
  fi
}

fail_step() {
  echo "step ${current_step} failed: $*" >&2
  explained=true
  exit 1
}

fail_api() {
  echo "step ${current_step} failed: $*" >&2
  echo "response body:" >&2
  lifecycle_redact "$(api_json 2>/dev/null || true)" >&2 || true
  explained=true
  exit 1
}

redact_cmd() {
  "$@" 2>&1 | lifecycle_redact_stream || true
}

hub_smoke_dump() {
  local ctx
  for ctx in "kind-${HUB_CLUSTER}" "kind-${SPOKE_CLUSTER}"; do
    echo "=== ${ctx} ==="
    redact_cmd kubectl --context "${ctx}" get pods -A -o wide
    redact_cmd kubectl --context "${ctx}" get "${LIFECYCLE_INSTANCE_GVK}" -A -o wide
    kubectl --context "${ctx}" get events -A --sort-by=.lastTimestamp 2>&1 | tail -n 80 | lifecycle_redact_stream || true
  done
  # The databases the run made so far: the source, and the restore copy once
  # the restore step has named it.
  local db_names=()
  if [[ -n "${db_name}" ]]; then
    db_names=("${db_name}" ${restore_name:+"${restore_name}"})
  fi
  if [[ -n "${db_name}" ]]; then
    # A database that never becomes Ready fails inside its own pods, which
    # the listings above only name. Every container's log and the pods'
    # conditions say why.
    echo "=== database pods in ${NAMESPACE} ==="
    redact_cmd spoke_kube -n "${NAMESPACE}" get pods -o wide
    redact_cmd spoke_kube -n "${NAMESPACE}" describe pods
    redact_cmd spoke_kube -n "${NAMESPACE}" logs --all-containers --prefix --tail=100 \
      -l "$(lifecycle_instance_selector "${db_names[@]}")"
  fi
  if [[ -n "${db_name}" && "${ENGINE}" == postgresql ]]; then
    # A PostgreSQL database that never becomes Ready, or a restore copy that
    # comes up empty, says why in the operator's own objects: the cluster's
    # conditions and the data source the agent seeded it with, and the backups
    # and restores the operator ran for it.
    local name grep_args=()
    for name in "${db_names[@]}"; do
      grep_args+=(-e "${name}")
    done
    echo "=== postgresql objects in ${NAMESPACE} ==="
    redact_cmd spoke_kube -n "${NAMESPACE}" get perconapgclusters,perconapgbackups,perconapgrestores -o wide
    for name in "${db_names[@]}"; do
      redact_cmd spoke_kube -n "${NAMESPACE}" get perconapgcluster "${name}" -o yaml
    done
    echo "=== postgresql operator and provider ==="
    redact_cmd spoke_kube -n "${HUB_NS}" logs deploy/pds-pg-operator --tail=3000 \
      | grep -F "${grep_args[@]}" | tail -n 300 || true
    redact_cmd spoke_kube -n "${HUB_NS}" logs deploy/pds-provider-postgresql --tail=3000 \
      | grep -F "${grep_args[@]}" | tail -n 150 || true
    # The monitoring step needs postgres_exporter's series under the database's
    # cluster label. The series monitoring holds say whether the exporter was
    # never added or reports under another label.
    echo "=== database monitoring ==="
    redact_cmd spoke_kube -n "${HUB_NS}" exec pmm-0 -- sh -c \
      'for q in "count by (cluster, service_name, node_name) (pg_up)" "count by (cluster, service_name) (pg_stat_activity_count)" "count by (job, agent_type) (up)"; do curl -sS -G --data-urlencode "query=$q" http://127.0.0.1:9090/prometheus/api/v1/query; echo; done'
  fi
  if [[ -n "${db_name}" && "${ENGINE}" == mysql ]]; then
    # The alert step needs mysql_up and the connections series. The sidecar's
    # log and the series monitoring holds say which side lost them.
    echo "=== database monitoring ==="
    redact_cmd spoke_kube -n "${NAMESPACE}" logs -c pmm-client --prefix --tail=400 \
      -l "app.kubernetes.io/instance=${db_name},app.kubernetes.io/component=pxc" \
      | grep -v 'metrics buckets' || true
    redact_cmd spoke_kube -n "${HUB_NS}" exec pmm-0 -- sh -c \
      'for q in "count by (cluster, service_name, node_name) (mysql_up)" "count by (cluster, service_name) (mysql_global_status_threads_connected)" "count by (job, agent_type) (up)"; do curl -sS -G --data-urlencode "query=$q" http://127.0.0.1:9090/prometheus/api/v1/query; echo; done'
    # MySQL refusing the sidecar's monitor password means a Secret and the
    # database disagree. Who wrote each Secret and when, never its data, and
    # what the operator and the provider did to this database say which moved.
    echo "=== database secrets and operators ==="
    redact_cmd spoke_kube -n "${NAMESPACE}" get secrets -o go-template='{{range .items}}{{.metadata.name}} created={{.metadata.creationTimestamp}}{{range .metadata.managedFields}} [{{.manager}} {{.operation}} {{.time}}]{{end}}{{"\n"}}{{end}}'
    redact_cmd spoke_kube -n "${HUB_NS}" logs deploy/pds-operator --tail=3000 \
      | grep -F "${db_name}" | tail -n 300 || true
    redact_cmd spoke_kube -n "${HUB_NS}" logs deploy/pds-provider-pxc --tail=3000 \
      | grep -F "${db_name}" | tail -n 150 || true
    # Which monitor accounts exist and when their passwords last changed, and
    # whether the password each container was started with logs in. Only the
    # answers are printed, never a password.
    redact_cmd spoke_kube -n "${NAMESPACE}" exec "${db_name}-pxc-0" -c pxc -- sh -c '
      MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -e "SELECT user, host, plugin, account_locked, password_last_changed FROM mysql.user WHERE user = \"monitor\""
      if out="$(MYSQL_PWD="$MONITOR_PASSWORD" mysql -umonitor -h"$(hostname -f)" -N -e "SELECT 1" 2>&1)"; then
        echo "monitor login from the member: accepted"
      else
        echo "monitor login from the member: refused, $(printf "%s" "$out" | grep -oE "ERROR [0-9]+" | head -n 1)"
      fi'
    # The exporter connects with what the sidecar registered with monitoring,
    # which can differ from the password the sidecar holds now.
    redact_cmd spoke_kube -n "${NAMESPACE}" exec "${db_name}-pxc-0" -c pmm-client -- \
      sh -c 'pmm-admin status; pmm-admin list'
    # Monitoring writes the monitor password unquoted into the exporter's
    # my.cnf, and the exporter's ini parser cuts a value at # or ;. Only
    # whether the password holds one of them is printed.
    redact_cmd spoke_kube -n "${NAMESPACE}" exec "${db_name}-pxc-0" -c pmm-client -- sh -c '
      case "$DB_PASSWORD" in
        *"#"* | *";"*) echo "monitor login has a character the exporter cuts at: yes" ;;
        *) echo "monitor login has a character the exporter cuts at: no" ;;
      esac'
    local member_hash sidecar_hash
    member_hash="$(spoke_kube -n "${NAMESPACE}" exec "${db_name}-pxc-0" -c pxc -- \
      sh -c 'printf %s "$MONITOR_PASSWORD" | sha256sum' 2>/dev/null || true)"
    sidecar_hash="$(spoke_kube -n "${NAMESPACE}" exec "${db_name}-pxc-0" -c pmm-client -- \
      sh -c 'printf %s "$DB_PASSWORD" | sha256sum' 2>/dev/null || true)"
    if [[ -z "${member_hash}" || -z "${sidecar_hash}" ]]; then
      echo "monitor password in the member and the sidecar: could not compare"
    elif [[ "${member_hash}" == "${sidecar_hash}" ]]; then
      echo "monitor password in the member and the sidecar: same"
    else
      echo "monitor password in the member and the sidecar: different"
    fi
  fi
  # A member that stays Pending is usually short of CPU on every node.
  echo "=== ${SPOKE_CLUSTER} node allocations ==="
  kubectl --context "kind-${SPOKE_CLUSTER}" describe nodes 2>&1 \
    | grep -E '^Name:|^  (cpu|memory) +[0-9]|^Allocatable:|^Allocated resources:' || true
  echo "=== hub adapter ==="
  redact_cmd hub_kube -n "${HUB_NS}" logs deploy/pds-adapter --tail=200
  echo "=== connected environment agent ==="
  redact_cmd spoke_kube -n "${HUB_NS}" logs deploy/pds-adapter --tail=200
  echo "=== flux ==="
  redact_cmd spoke_kube -n flux-system get gitrepositories.source.toolkit.fluxcd.io,kustomizations.kustomize.toolkit.fluxcd.io -o yaml
  echo "=== gitea ==="
  docker logs "${GITEA_CONTAINER}" --tail 200 2>&1 | lifecycle_redact_stream || true
}

finish() {
  local rc=$?
  trap - EXIT
  if [[ "${rc}" -ne 0 && "${dump_only}" != true ]]; then
    if [[ "${explained}" != true ]]; then
      echo "step ${current_step} failed" >&2
    fi
    hub_smoke_dump || true
  fi
  # A failure between LOCK and UNLOCK would leave the account locked on a kept
  # lab. This variant returns instead of exiting, so the trap is not re-entered.
  if [[ "${monitor_locked}" == true && -n "${db_name}" && "${dump_only}" != true ]]; then
    MONITOR_BOUND=60 run_monitor_account "${db_name}" UNLOCK >&2 || true
  fi
  if [[ -n "${tmp}" ]]; then
    rm -rf "${tmp}"
  fi
  exit "${rc}"
}

if [[ "${DUMP}" == true ]]; then
  dump_only=true
  hub_smoke_dump
  exit 0
fi

trap finish EXIT

step "check tools"
for tool in docker kind kubectl helm flux git curl python3 node npm; do
  command -v "${tool}" >/dev/null 2>&1 || fail_step "missing required tool: ${tool}"
done
# The runner is amd64, so Connect installs monitoring on the connected
# cluster. An arm64 laptop is the lab's own exception and is not forced here.
echo "machine $(uname -m); connected environment workers ${LAB_SPOKE_WORKERS}"

step "preload images"
# The lab starts these on the host. kind uses the node image from the local
# docker store, and Gitea is a container. Platform images are pulled by
# Connect. This is not a second mirror.
docker pull "${K8S_IMAGE}"
docker pull "${GITEA_IMAGE}"

step "lab"
export LAB_SPOKE_WORKERS
if [[ "${FLUX}" == true ]]; then
  export LAB_CONNECT=flux
fi
"${REPO_ROOT}/scripts/up.sh"

step "sign in"
tmp="$(mktemp -d)"
api_body="${tmp}/body"
api_cookies="${tmp}/cookies"
lab_wait_login "${UI_PORT}" "${BOOTSTRAP_ORGANIZATION}" "${BOOTSTRAP_PASSWORD}" "${api_cookies}" \
  || fail_step "could not sign in as ${BOOTSTRAP_ORGANIZATION}"
echo "signed in as ${BOOTSTRAP_ORGANIZATION}"

# up.sh returns once the environment is connected, so the lab is ready here.
if [[ "${LAB_ONLY}" == true ]]; then
  echo "lab ready: environment ${SPOKE_CLUSTER} connected, no database created"
  exit 0
fi

require_code() {
  local got="$1" want="$2" what="$3"
  [[ "${got}" == "${want}" ]] || fail_api "${what} answered ${got}, want ${want}"
}

# Sets API_CODE. Curl failures exit in this shell so the step name is kept.
# The hub is reached through a port-forward the lab restarts when it drops, a
# starved runner can hold a reply past curl's limit, and the hub answers 5xx
# while it restarts. A read is tried again for about a minute. A write is
# not: it may have landed.
api_call() {
  local method="$1" path="$2" body="${3:-}" attempt answered
  for attempt in 1 2 3 4 5 6 7; do
    answered=false
    if API_CODE="$(api "${method}" "${path}" "${body}")"; then
      answered=true
      [[ "${API_CODE}" == 5* && "${method}" == GET ]] || return 0
    fi
    [[ "${method}" == GET && "${attempt}" -lt 7 ]] || break
    echo "  ${method} ${path} answered ${API_CODE:-nothing}, trying again" >&2
    sleep 10
  done
  [[ "${answered}" == true ]] && return 0
  fail_api "${method} ${path} failed: curl error"
}

# The operator reports a failed reconcile as failed, and a new database hits
# one while its proxy is still starting: an update needs the proxy to find the
# primary. Only a failed status that lasts FAILED_GRACE ends the wait early.
wait_ready() {
  local db_id="$1"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local code db_status failed_since=""
  while :; do
    api_call GET "/api/databases/${db_id}"
    code="${API_CODE}"
    [[ "${code}" == "200" ]] || fail_api "GET /api/databases/${db_id} answered ${code}, want 200"
    db_status="$(lifecycle_string_field "$(api_json)" status)" || fail_api "GET /api/databases/${db_id} carries no status"
    echo "  status: ${db_status}"
    case "$(lifecycle_ready_outcome "${db_status}")" in
      ready) return 0 ;;
      failed)
        failed_since="${failed_since:-$(date +%s)}"
        if (( $(date +%s) - failed_since >= FAILED_GRACE )); then
          fail_api "${db_id} stayed in status failed for ${FAILED_GRACE}s"
        fi
        ;;
      *) failed_since="" ;;
    esac
    if (( $(date +%s) >= deadline )); then
      fail_api "${db_id} did not reach ready within ${LIFECYCLE_TIMEOUT}s, last status ${db_status}"
    fi
    sleep 10
  done
}

# The hub adds monitoring to a database only if its environment has already
# reported a MonitoringConfig, and connect installs monitoring after the
# environment has joined. A database created sooner never gets monitoring, so
# the create waits for the environments list to say it will.
wait_environment_monitored() {
  local environment="$1"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  while :; do
    api_call GET /api/environments
    require_code "${API_CODE}" 200 "GET /api/environments"
    if lifecycle_environment_monitored "$(api_json)" "${environment}"; then
      echo "  ${environment} reports monitoring"
      return 0
    fi
    echo "  ${environment} does not report monitoring yet"
    if (( $(date +%s) >= deadline )); then
      fail_api "${environment} did not report monitoring within ${LIFECYCLE_TIMEOUT}s"
    fi
    sleep 10
  done
}

read_instance() {
  local name="$1"
  spoke_kube -n "${NAMESPACE}" get "${LIFECYCLE_INSTANCE_GVK}" "${name}" -o json
}

assert_dev_instance() {
  local name="$1"
  local json preset cpu mem storage managed profile
  json="$(read_instance "${name}")" || fail_step "could not read Instance ${name}"
  preset="$(printf '%s' "${json}" | python3 -c 'import json,sys
data=json.load(sys.stdin)
print(((data.get("metadata") or {}).get("annotations") or {}).get("openeverest.io/instance-preset", ""))
')"
  cpu="$(lifecycle_instance_cpu "${json}")" || fail_step "Instance ${name} has no cpu"
  mem="$(printf '%s' "${json}" | python3 -c 'import json,sys
data=json.load(sys.stdin)
eng=((data.get("spec") or {}).get("components") or {}).get("engine") or {}
print((((eng.get("resources") or {}).get("limits") or {}).get("memory") or ""))
')"
  storage="$(printf '%s' "${json}" | python3 -c 'import json,sys
data=json.load(sys.stdin)
eng=((data.get("spec") or {}).get("components") or {}).get("engine") or {}
print(((eng.get("storage") or {}).get("size") or ""))
')"
  managed="$(printf '%s' "${json}" | python3 -c 'import json,sys
data=json.load(sys.stdin)
print(((data.get("metadata") or {}).get("labels") or {}).get("app.kubernetes.io/managed-by", ""))
')"
  profile="$(lifecycle_instance_profile "${json}")"
  [[ "${preset}" == "${PRESET}" ]] || fail_step "Instance ${name} preset annotation is ${preset}, want ${PRESET}"
  [[ "${cpu}" == "1" && "${mem}" == "2Gi" && "${storage}" == "20Gi" ]] || \
    fail_step "Instance ${name} sizing is cpu=${cpu} memory=${mem} storage=${storage}, want 1 / 2Gi / 20Gi"
  [[ "${managed}" == "pds" ]] || fail_step "Instance ${name} managed-by is ${managed:-empty}, want pds"
  [[ "${profile}" == "dev" ]] || fail_step "Instance ${name} profile is ${profile:-empty}, want dev"
  lifecycle_assert_instance_spec "${json}" 1 1 "${PROVIDER}" \
    || fail_step "Instance ${name} spec did not match cpu=1 replicas=1 provider=${PROVIDER}"
  echo "Instance ${name} copied ${PRESET} (1 / 2Gi / 20Gi) and is managed by pds"
}

# The agent writes a new database's users Secret before the operator can, so
# the monitor login holds nothing the monitoring exporter cuts it at: # and ;
# end an ini value, and $ is expanded. The password itself is never printed.
assert_users_secret() {
  local name="$1" secret managed encoded cuts
  secret="everest-secrets-${name}"
  managed="$(spoke_kube -n "${NAMESPACE}" get secret "${secret}" \
    -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')" \
    || fail_step "could not read the users Secret of ${name}"
  [[ "${managed}" == "pds" ]] \
    || fail_step "the users Secret of ${name} was not written by the agent, managed-by is ${managed:-empty}"
  # An empty value would count no characters and pass, so a missing key fails
  # here. The value stays in this variable and is never printed.
  encoded="$(spoke_kube -n "${NAMESPACE}" get secret "${secret}" -o jsonpath='{.data.monitor}')" \
    || fail_step "could not read the monitor login of ${name}"
  [[ -n "${encoded}" ]] || fail_step "the users Secret of ${name} has no monitor login"
  # Only the count of those characters leaves the pipe.
  cuts="$(printf '%s' "${encoded}" | base64 -d | tr -cd '#;$' | wc -c)" \
    || fail_step "could not read the monitor login of ${name}"
  [[ "${cuts// /}" == "0" ]] \
    || fail_step "the monitor login of ${name} has a character the exporter cuts at"
  echo "monitor login has a character the exporter cuts at: no"
}

wait_instance() {
  local name="$1"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  while :; do
    if spoke_kube -n "${NAMESPACE}" get "${LIFECYCLE_INSTANCE_GVK}" "${name}" >/dev/null 2>&1; then
      return 0
    fi
    echo "  Instance ${name} not on ${SPOKE_CLUSTER} yet"
    if (( $(date +%s) >= deadline )); then
      fail_step "Instance ${name} did not appear on ${SPOKE_CLUSTER} within ${LIFECYCLE_TIMEOUT}s"
    fi
    sleep 10
  done
}

# Scale is done when the object and the heartbeat both show it, and the
# heartbeat has gone not-ready and come back. The first ready after the
# write is still the size from before Flux applied it. Shrinking keeps the
# database serving, so its status may never leave ready: pass false as the
# fifth argument, and the heartbeat's member count stands in for it.
wait_scaled() {
  local db_id="$1" name="$2" cpu="$3" replicas="$4" await_non_ready="${5:-true}"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local code json cluster_cpu cluster_replicas api_cpu api_members api_status seen_non_ready
  seen_non_ready=false
  [[ "${await_non_ready}" == true ]] || seen_non_ready=true
  while :; do
    json=""
    json="$(read_instance "${name}" 2>/dev/null || true)"
    cluster_cpu=""
    cluster_replicas=""
    if [[ -n "${json}" ]]; then
      cluster_cpu="$(lifecycle_instance_cpu "${json}" 2>/dev/null || true)"
      cluster_replicas="$(lifecycle_instance_replicas "${json}" 2>/dev/null || true)"
    fi
    api_call GET "/api/databases/${db_id}"
    code="${API_CODE}"
    [[ "${code}" == "200" ]] || fail_api "GET /api/databases/${db_id} answered ${code}, want 200"
    api_cpu="$(json_field "$(api_json)" cpu 2>/dev/null || true)"
    api_members="$(json_field "$(api_json)" members 2>/dev/null || true)"
    api_status="$(json_field "$(api_json)" status 2>/dev/null || true)"
    echo "  cluster cpu ${cluster_cpu:-missing} replicas ${cluster_replicas:-missing}; heartbeat cpu ${api_cpu:-missing} members ${api_members:-missing} status ${api_status:-missing}"
    case "$(lifecycle_ready_outcome "${api_status}")" in
      failed) fail_api "${db_id} reached status failed" ;;
      wait) seen_non_ready=true ;;
    esac
    if [[ "${cluster_cpu}" == "${cpu}" && "${cluster_replicas}" == "${replicas}" \
      && "${api_cpu}" == "${cpu}" && "${api_members}" == "${replicas}" \
      && "$(lifecycle_return_ready_outcome "${api_status}" "${seen_non_ready}")" == "ready" ]]; then
      lifecycle_assert_instance_spec "${json}" "${cpu}" "${replicas}" "${PROVIDER}" \
        || fail_step "Instance ${name} spec did not match cpu=${cpu} replicas=${replicas}"
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      fail_step "${db_id} did not show cpu ${cpu} and ${replicas} members on the cluster and in the heartbeat within ${LIFECYCLE_TIMEOUT}s"
    fi
    sleep 10
  done
}

wait_listed() {
  local db_id="$1"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local code
  while :; do
    api_call GET "/api/databases/${db_id}"
    code="${API_CODE}"
    if [[ "${code}" == "200" ]]; then
      echo "  heartbeat lists ${db_id}"
      return 0
    fi
    echo "  list ${db_id}: HTTP ${code}"
    [[ "${code}" == "404" ]] || fail_api "GET /api/databases/${db_id} answered ${code}, want 200"
    if (( $(date +%s) >= deadline )); then
      fail_step "${db_id} did not appear in the heartbeat within ${LIFECYCLE_TIMEOUT}s"
    fi
    sleep 10
  done
}

# The hand-built Instance is not in Git. Scale reaches it as a work item,
# and the field PDS does not model has to still be on the object afterwards.
wait_unmodeled() {
  local name="$1" cpu="$2" replicas="$3"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local json got_cpu got_replicas got_param
  while :; do
    json="$(read_instance "${name}" 2>/dev/null || true)"
    got_cpu=""
    got_replicas=""
    got_param=""
    if [[ -n "${json}" ]]; then
      got_cpu="$(lifecycle_instance_cpu "${json}" 2>/dev/null || true)"
      got_replicas="$(lifecycle_instance_replicas "${json}" 2>/dev/null || true)"
      got_param="$(lifecycle_instance_parameter "${json}" "${LIFECYCLE_UNMODELED_PARAM}" 2>/dev/null || true)"
    fi
    echo "  cpu ${got_cpu:-missing} replicas ${got_replicas:-missing} ${LIFECYCLE_UNMODELED_PARAM} ${got_param:-missing}"
    if [[ "${got_cpu}" == "${cpu}" && "${got_replicas}" == "${replicas}" && "${got_param}" == "${LIFECYCLE_UNMODELED_VALUE}" ]]; then
      lifecycle_assert_instance_spec "${json}" "${cpu}" "${replicas}" "${PROVIDER}" \
        || fail_step "Instance ${name} spec did not match after the work item"
      lifecycle_assert_instance_parameter "${json}" "${LIFECYCLE_UNMODELED_PARAM}" "${LIFECYCLE_UNMODELED_VALUE}" \
        || fail_step "unmodeled ${LIFECYCLE_UNMODELED_PARAM} did not survive the scale"
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      fail_step "Instance ${name} did not keep ${LIFECYCLE_UNMODELED_PARAM}=${LIFECYCLE_UNMODELED_VALUE} at cpu ${cpu} replicas ${replicas}"
    fi
    sleep 10
  done
}

schedule_minute=15
schedule_hour=3
schedule_copies=4

schedule_matches() {
  local json="$1"
  local period hour minute copies enabled
  period="$(json_field "${json}" backupSchedule.period)" || return 1
  hour="$(json_field "${json}" backupSchedule.hour)" || return 1
  minute="$(json_field "${json}" backupSchedule.minute)" || return 1
  copies="$(json_field "${json}" backupSchedule.retentionCopies)" || return 1
  enabled="$(json_field "${json}" backupSchedule.enabled)" || return 1
  [[ "${period}" == "daily" && "${hour}" == "${schedule_hour}" && "${minute}" == "${schedule_minute}" \
    && "${copies}" == "${schedule_copies}" && "${enabled}" == "true" ]]
}

wait_schedule() {
  local db_id="$1"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local code db_status
  while :; do
    api_call GET "/api/databases/${db_id}"
    code="${API_CODE}"
    [[ "${code}" == "200" ]] || fail_api "GET /api/databases/${db_id} answered ${code}, want 200"
    db_status="$(json_field "$(api_json)" status 2>/dev/null || true)"
    if schedule_matches "$(api_json)"; then
      echo "  heartbeat schedule daily ${schedule_hour}:$(printf '%02d' "${schedule_minute}") retention ${schedule_copies}, status ${db_status:-missing}"
      if [[ "${db_status}" == "ready" ]]; then
        return 0
      fi
    else
      echo "  heartbeat schedule absent, status ${db_status:-missing}"
    fi
    if [[ "${db_status}" == "failed" ]]; then
      fail_api "${db_id} reached status failed while waiting for the schedule"
    fi
    if (( $(date +%s) >= deadline )); then
      fail_step "${db_id} did not report the backup schedule within ${LIFECYCLE_TIMEOUT}s"
    fi
    sleep 10
  done
}

wait_backup() {
  local db_id="$1" backup_id="$2"
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local code backup_state
  while :; do
    api_call GET "/api/databases/${db_id}/backups"
    code="${API_CODE}"
    [[ "${code}" == "200" ]] || fail_api "GET backups answered ${code}, want 200"
    backup_state=""
    backup_state="$(lifecycle_backup_status "$(api_json)" "${backup_id}" 2>/dev/null || true)"
    echo "  backup ${backup_id}: ${backup_state:-missing}"
    case "$(lifecycle_backup_poll_outcome "${backup_state}")" in
      succeeded) return 0 ;;
      failed) fail_api "backup ${backup_id} reached status failed" ;;
    esac
    if (( $(date +%s) >= deadline )); then
      fail_api "backup ${backup_id} did not succeed within ${LIFECYCLE_TIMEOUT}s, last status ${backup_state:-missing}"
    fi
    sleep 10
  done
}

# Monitoring reads mysql_up through an account of its own. Locking that
# account makes mysql_up 0 while the pods and mysqld stay up, which is what
# service_down watches. A pod that vanished would report no data, and the
# seeded rule treats no data as healthy. Open sessions are killed so the lock
# takes effect on the next scrape.
run_monitor_account() {
  local name="$1" action="$2"
  local pod
  pod="$(spoke_kube -n "${NAMESPACE}" get pods \
    -l "app.kubernetes.io/instance=${name},app.kubernetes.io/component=pxc" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [[ -z "${pod}" ]]; then
    echo "no pxc pod found for ${name} on ${SPOKE_CLUSTER}" >&2
    return 2
  fi
  bounded "${MONITOR_BOUND:-120}" kubectl --context "kind-${SPOKE_CLUSTER}" -n "${NAMESPACE}" exec "${pod}" -c pxc -- sh -c '
    set -e
    export MYSQL_PWD="${MYSQL_ROOT_PASSWORD}"
    mysql -uroot -e "ALTER USER \"$1\"@\"%\" ACCOUNT $2" || exit 1
    if [ "$2" = LOCK ]; then
      mysql -uroot -N -e "SELECT id FROM information_schema.processlist WHERE user = \"$1\"" |
        while read -r id; do mysql -uroot -e "KILL $id" || true; done
    fi
  ' sh "${ALERT_MONITOR_USER}" "${action}" >/dev/null || {
    echo "could not ${action} the ${ALERT_MONITOR_USER} account on ${pod}" >&2
    return 1
  }
  case "${action}" in
    LOCK) echo "  ${ALERT_MONITOR_USER} account locked on ${pod}" ;;
    *) echo "  ${ALERT_MONITOR_USER} account unlocked on ${pod}" ;;
  esac
}

# monitor_locked tells the exit trap that the account may still be locked. It
# is set before LOCK is sent, because an ALTER USER can commit while the exec
# connection drops. Only a failure before anything was sent (status 2) or a
# successful UNLOCK clears it. The member's liveness probe signs in as the
# same account, so a long lock restarts mysqld: UNLOCK keeps trying until
# mysqld answers again.
set_monitor_account() {
  local action="$2" msg rc=0
  local deadline=$(( $(date +%s) + ALERT_TIMEOUT ))
  [[ "${action}" == LOCK ]] && monitor_locked=true
  while :; do
    rc=0
    msg="$(run_monitor_account "$@" 2>&1)" || rc=$?
    [[ "${rc}" -ne 0 && "${action}" == UNLOCK ]] || break
    (( $(date +%s) < deadline )) || break
    echo "  ${ALERT_MONITOR_USER} account not unlocked yet, trying again" >&2
    sleep 10
  done
  if [[ "${rc}" -eq 2 ]] || { [[ "${rc}" -eq 0 && "${action}" != LOCK ]]; }; then
    monitor_locked=false
  fi
  [[ "${rc}" -eq 0 ]] || fail_step "${msg}"
  printf '%s\n' "${msg}"
}

# Waits until monitoring scrapes the database's own service. Node series, and
# HAProxy's for MySQL, exist earlier, so this waits for a connections reading,
# which only the engine's exporter produces. The MySQL alert step needs it too:
# locking the account before pmm-client has added the service means mysql_up
# never exists, and the rule, which reads no data as healthy, never fires.
wait_monitored() {
  local db_id="$1"
  local deadline=$(( $(date +%s) + ALERT_TIMEOUT ))
  while :; do
    api_call GET "/api/databases/${db_id}/metrics?metrics=connections"
    require_code "${API_CODE}" 200 "GET /api/databases/${db_id}/metrics"
    if lifecycle_metrics_flowing "$(api_json)"; then
      echo "  monitoring has ${db_id}"
      return 0
    fi
    echo "  monitoring has no ${ENGINE_LABEL} reading for ${db_id} yet"
    if (( $(date +%s) >= deadline )); then
      fail_api "monitoring had no ${ENGINE_LABEL} reading for ${db_id} within ${ALERT_TIMEOUT}s"
    fi
    sleep 10
  done
}

# Waits until Grafana in PMM lists all nine PDS rules with health ok. A rule
# with a wrong series or syntax reads as no data, which the rules treat as
# healthy, so only the rule list says Grafana loaded and evaluated them. The
# admin password goes to curl on stdin, never on a command line.
PMM_ADMIN_SECRET="${PMM_ADMIN_SECRET:-pmm-secret}"
PDS_RULE_COUNT=9
wait_alert_rules_healthy() {
  local deadline=$(( $(date +%s) + ALERT_TIMEOUT ))
  local pass body
  pass="$(spoke_kube -n "${HUB_NS}" get secret "${PMM_ADMIN_SECRET}" \
    -o jsonpath='{.data.PMM_ADMIN_PASSWORD}' | base64 -d)" \
    || fail_step "could not read the PMM admin password from ${PMM_ADMIN_SECRET}"
  while :; do
    body="$(printf 'user = "admin:%s"\n' "${pass}" | spoke_kube -n "${HUB_NS}" exec -i pmm-0 -- \
      curl -sS -K - http://127.0.0.1:3000/graph/api/prometheus/grafana/api/v1/rules 2>&1 || true)"
    if lifecycle_alert_rules_healthy "${body}" "${PDS_RULE_COUNT}" 2>/dev/null; then
      echo "  Grafana lists ${PDS_RULE_COUNT} PDS rules, all healthy"
      return 0
    fi
    echo "  Grafana does not list ${PDS_RULE_COUNT} healthy PDS rules yet"
    if (( $(date +%s) >= deadline )); then
      lifecycle_alert_rules_healthy "${body}" "${PDS_RULE_COUNT}" || true
      fail_step "Grafana did not list ${PDS_RULE_COUNT} healthy PDS rules within ${ALERT_TIMEOUT}s"
    fi
    sleep 10
  done
}

# Waits for the alerts route to show the rule firing for the database
# (want=firing) or no longer showing it (want=clear). A clear only counts
# while the environment still reports live, so an environment that stopped
# reporting never reads as a cleared alert.
wait_alert() {
  local rule="$1" db_id="$2" want="$3"
  local deadline=$(( $(date +%s) + ALERT_TIMEOUT ))
  local state firing
  while :; do
    api_call GET /api/alerts
    require_code "${API_CODE}" 200 "GET /api/alerts"
    state="$(lifecycle_environment_state "$(api_json)" "${SPOKE_CLUSTER}" 2>/dev/null || true)"
    firing=false
    if lifecycle_alert_firing "$(api_json)" "${rule}" "${db_id}"; then
      firing=true
    fi
    echo "  ${SPOKE_CLUSTER} alerts ${state:-missing}; ${rule} firing for ${db_id}: ${firing}"
    if [[ "${state}" == "live" ]]; then
      case "${want}:${firing}" in
        firing:true | clear:false) return 0 ;;
      esac
    fi
    if (( $(date +%s) >= deadline )); then
      fail_api "${rule} was not ${want} for ${db_id} within ${ALERT_TIMEOUT}s, ${SPOKE_CLUSTER} alerts ${state:-missing}"
    fi
    sleep 10
  done
}

# The join script's --update refuses while a database is outside Ready or a
# Backup or Restore has not finished, and the run itself starts some: the
# restore database gets a backup of its own, and the schedule fires on its own
# clock. Wait until none are left, and name what is when the wait runs out.
wait_work_finished() {
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local json unfinished="" last_error=""
  while :; do
    last_error=""
    if json="$(spoke_kube -n "${NAMESPACE}" get \
      "${LIFECYCLE_INSTANCE_GVK},${LIFECYCLE_BACKUP_GVK},${LIFECYCLE_RESTORE_GVK}" -o json 2>&1)" &&
      unfinished="$(lifecycle_unfinished_work "${json}" 2>&1)"; then
      if [[ -z "${unfinished}" ]]; then
        echo "  nothing is running in ${NAMESPACE}"
        return 0
      fi
      echo "  still running in ${NAMESPACE}:"
      sed 's/^/    /' <<<"${unfinished}"
    else
      last_error="could not read the databases, backups and restores in ${NAMESPACE}"
      echo "  ${last_error}"
    fi
    if (( $(date +%s) >= deadline )); then
      if [[ -n "${last_error}" ]]; then
        fail_step "${last_error} within ${LIFECYCLE_TIMEOUT}s"
      fi
      fail_step "still running in ${NAMESPACE} after ${LIFECYCLE_TIMEOUT}s: $(tr '\n' ';' <<<"${unfinished}")"
    fi
    sleep 10
  done
}

wait_gone() {
  local deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  local name left
  while :; do
    left=0
    for name in "$@"; do
      if spoke_kube -n "${NAMESPACE}" get "${LIFECYCLE_INSTANCE_GVK}" "${name}" >/dev/null 2>&1; then
        echo "  Instance ${name} still present"
        left=$((left + 1))
      else
        echo "  Instance ${name} gone"
      fi
    done
    if [[ "${left}" -eq 0 ]]; then
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      fail_step "${left} Instance(s) still on ${SPOKE_CLUSTER} after ${LIFECYCLE_TIMEOUT}s"
    fi
    sleep 10
  done
}

# The agent deletes the Secrets it can name after the PostgreSQL operator
# leaves them behind, on its following beats, so poll for them to be gone. The
# list mirrors postgresql.LeftoverSecrets. The per-member "-certs" Secrets end
# in a random suffix, so the agent cannot name them and they are not checked.
wait_secrets_gone() {
  local name="$1"
  local deadline=$(( $(date +%s) + 90 ))
  local left suffix
  while :; do
    left=""
    for suffix in pguser-postgres pguser-monitor pgbouncer cluster-cert replication-cert pgbackrest; do
      if spoke_kube -n "${NAMESPACE}" get secret "${name}-${suffix}" >/dev/null 2>&1; then
        left+=$'\n'"secret/${name}-${suffix}"
      fi
    done
    if [[ -z "${left//[$'\n ']/}" ]]; then
      echo "  no Secrets left behind for ${name}"
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      fail_step "Secrets left behind for ${name}: ${left//$'\n'/ }"
    fi
    sleep 5
  done
}

git_instance_file() {
  printf '%s/.local/lab-git/clusters/%s/instances/%s.yaml' "${REPO_ROOT}" "${SPOKE_CLUSTER}" "$1"
}

# A database the hub only learned from a heartbeat must not gain a file.
# The hub writes that file itself when it takes the Git path.
assert_git_stays_absent() {
  local name="$1"
  local path start now
  path="$(git_instance_file "${name}")"
  start="$(date +%s)"
  while :; do
    if [[ -e "${path}" ]]; then
      fail_step "Instance ${name} is in Git at ${path}. The hub wrote it instead of handing the change to the agent."
    fi
    now="$(date +%s)"
    if (( now - start >= 20 )); then
      echo "  Git has no ${name}.yaml"
      return 0
    fi
    sleep 5
  done
}

wait_git_absent() {
  local name="$1"
  local path deadline
  path="$(git_instance_file "${name}")"
  deadline=$(( $(date +%s) + LIFECYCLE_TIMEOUT ))
  while [[ -e "${path}" ]]; do
    echo "  Git still has ${name}.yaml"
    if (( $(date +%s) >= deadline )); then
      fail_step "Instance ${name} stayed in Git at ${path}"
    fi
    sleep 5
  done
  echo "  Git has no ${name}.yaml"
}

# Flux prunes on a 15s interval. One empty poll can land between a bad
# delete and the object coming back from Git.
wait_stay_gone() {
  local hold=35
  echo "  holding ${hold}s so a Git rewrite cannot recreate them"
  sleep "${hold}"
  local name
  for name in "$@"; do
    if spoke_kube -n "${NAMESPACE}" get "${LIFECYCLE_INSTANCE_GVK}" "${name}" >/dev/null 2>&1; then
      fail_step "Instance ${name} came back on ${SPOKE_CLUSTER}"
    fi
    echo "  Instance ${name} stayed gone"
  done
}

step "create a ${ENGINE_LABEL} Dev database"
# The managed release leaves monitoring to Git, which this suite does not
# render, so the environment never reports it.
if [[ "${FLUX}" != true ]]; then
  wait_environment_monitored "${SPOKE_CLUSTER}"
fi
stamp="$(date +%s)"
db_name="hub-${stamp}"
api_call POST /api/databases "$(lifecycle_create_body "${db_name}" "${SPOKE_CLUSTER}" "${ENGINE}")"
code="${API_CODE}"
require_code "${code}" 201 "POST /api/databases"
db_id="$(lifecycle_string_field "$(api_json)" id)" || fail_api "the create response carries no id"
engine="$(lifecycle_string_field "$(api_json)" engine)" || fail_api "the create response carries no engine"
environment="$(lifecycle_string_field "$(api_json)" environment)" || fail_api "the create response carries no environment"
NAMESPACE="$(lifecycle_string_field "$(api_json)" namespace)" || fail_api "the create response carries no namespace"
[[ "${engine}" == "${ENGINE}" ]] || fail_api "create engine ${engine}, want ${ENGINE}"
[[ "${environment}" == "${SPOKE_CLUSTER}" ]] || fail_api "create environment ${environment}, want ${SPOKE_CLUSTER}"
[[ "$(lifecycle_name_from_id "${db_id}")" == "${db_name}" ]] || fail_api "create id ${db_id} does not name ${db_name}"
[[ "${db_id}" == "${environment}/${NAMESPACE}.${db_name}" ]] || fail_api "create id ${db_id}, want ${environment}/${NAMESPACE}.${db_name}"
echo "created ${db_id}"
wait_instance "${db_name}"
assert_dev_instance "${db_name}"

step "wait for Ready"
wait_ready "${db_id}"
echo "${db_id} is ready"

if [[ "${FLUX}" == true ]]; then
  step "delete the database"
  api_call DELETE "/api/databases/${db_id}"
  require_code "${API_CODE}" 204 "DELETE ${db_id}"
  wait_git_absent "${db_name}"
  wait_gone "${db_name}"
  step "summary"
  echo "hub:       kind-${HUB_CLUSTER}"
  echo "cluster:   kind-${SPOKE_CLUSTER} (Flux and the pds HelmRelease, no join script)"
  echo "created:   ${db_id}, Ready, then deleted"
  echo "hub smoke passed"
  exit 0
fi

# The monitor login goes first: one MySQL's exporter cannot use would
# otherwise show only as a monitoring wait that times out.
if lifecycle_has_alert_step "${ENGINE}"; then
  step "check the monitor login"
  assert_users_secret "${db_name}"
fi

step "check monitoring"
wait_monitored "${db_id}"
echo "monitoring reads ${ENGINE_LABEL} connections for ${db_id}"

step "check the alert rules"
wait_alert_rules_healthy

alert_summary="skipped for ${ENGINE_LABEL}"
if lifecycle_has_alert_step "${ENGINE}"; then
  step "fire and clear service_down"
  # Monitoring scrapes this database since the step above. Its alerts must be
  # readable too before the alert is provoked, or a pending read looks like a
  # missed alert.
  wait_alert service_down "${db_id}" clear
  set_monitor_account "${db_name}" LOCK
  wait_alert service_down "${db_id}" firing
  echo "service_down fired for ${db_id}"
  set_monitor_account "${db_name}" UNLOCK
  # A locked account may fail the pod's readiness check, so wait for Ready again.
  wait_ready "${db_id}"
  wait_alert service_down "${db_id}" clear
  echo "service_down cleared for ${db_id}"
  alert_summary="service_down fired and cleared for ${db_id}"
fi

# A member with 2 CPU cannot schedule on a 2 CPU node beside the node's own
# pods, so the members keep 1 CPU. Only the MySQL pass changes CPU, on the
# hand-built Instance below. The PostgreSQL pass never changes it.
step "scale members"
api_call POST "/api/databases/${db_id}/scale" "$(lifecycle_scale_body 1 3)"
code="${API_CODE}"
require_code "${code}" 200 "POST scale"
wait_scaled "${db_id}" "${db_name}" 1 3
echo "Instance ${db_name} scaled: cpu 1, replicas 3"
# A hosted runner cannot keep three members up beside the rest of the
# lifecycle, so the database shrinks back to one before the next step.
api_call POST "/api/databases/${db_id}/scale" "$(lifecycle_scale_body 1 1)"
code="${API_CODE}"
require_code "${code}" 200 "POST scale down"
wait_scaled "${db_id}" "${db_name}" 1 1 false
echo "Instance ${db_name} scaled: cpu 1, replicas 1"

hand_id=""
hand_name=""
if lifecycle_has_unmodeled_step "${ENGINE}"; then
  step "preserve an unmodeled Instance field"
  hand_name="hand-${stamp}"
  hand_id="${SPOKE_CLUSTER}/${NAMESPACE}.${hand_name}"
  preset_json="$(spoke_kube get instancepreset "${PRESET}" -o json)" \
    || fail_step "could not read InstancePreset ${PRESET}"
  hand_doc="$(lifecycle_hand_instance "${preset_json}" "${hand_name}" "${NAMESPACE}" "${LIFECYCLE_UNMODELED_PARAM}" "${LIFECYCLE_UNMODELED_VALUE}")" \
    || fail_step "could not build Instance ${hand_name}"
  printf '%s' "${hand_doc}" | spoke_kube apply -f - \
    || fail_step "could not create Instance ${hand_name}"
  echo "created Instance ${hand_name} with ${LIFECYCLE_UNMODELED_PARAM}=${LIFECYCLE_UNMODELED_VALUE}"
  wait_listed "${hand_id}"
  # One member at 2 CPU. Only the object is checked: a 2 CPU member does not
  # schedule on a hosted runner, and the three members above hold their nodes.
  api_call POST "/api/databases/${hand_id}/scale" "$(lifecycle_scale_body 2 1)"
  code="${API_CODE}"
  require_code "${code}" 200 "POST scale of ${hand_id}"
  wait_unmodeled "${hand_name}" 2 1
  assert_git_stays_absent "${hand_name}"
  echo "Instance ${hand_name} kept ${LIFECYCLE_UNMODELED_PARAM}=${LIFECYCLE_UNMODELED_VALUE} after scale"
  # Deleted now rather than with the others, so it does not hold a runner's
  # memory through the backup and the restore.
  api_call DELETE "/api/databases/${hand_id}"
  require_code "${API_CODE}" 204 "DELETE ${hand_id}"
  wait_git_absent "${hand_name}"
  wait_gone "${hand_name}"
  echo "deleted ${hand_id}"
fi

step "set a backup schedule"
schedule_body="$(printf '{"period":"daily","minute":%s,"hour":%s,"retentionCopies":%s}' \
  "${schedule_minute}" "${schedule_hour}" "${schedule_copies}")"
api_call POST "/api/databases/${db_id}/backup-schedule" "${schedule_body}"
code="${API_CODE}"
require_code "${code}" 200 "POST backup schedule"
schedule_matches "$(api_json)" || fail_api "the schedule response did not echo the schedule"
echo "schedule accepted: daily ${schedule_hour}:$(printf '%02d' "${schedule_minute}"), ${schedule_copies} copies"
wait_schedule "${db_id}"
echo "heartbeat reports the schedule for ${db_id}"

step "back up"
api_call POST "/api/databases/${db_id}/backup"
code="${API_CODE}"
require_code "${code}" 202 "POST backup"
backup_id="$(lifecycle_string_field "$(api_json)" id)" || fail_api "the backup response carries no id"
wait_backup "${db_id}" "${backup_id}"
echo "backup ${backup_id} succeeded"

step "restore into a new database"
restore_name="rest-${stamp}"
api_call POST "/api/databases/${db_id}/restore" "$(lifecycle_restore_body "${backup_id}" "${restore_name}")"
code="${API_CODE}"
require_code "${code}" 202 "POST restore"
restore_id="$(json_field "$(api_json)" id)" || fail_api "the restore response carries no id"
[[ "${restore_id}" != "${db_id}" ]] || fail_api "restore id ${restore_id} is the source database"
[[ "$(lifecycle_name_from_id "${restore_id}")" == "${restore_name}" ]] || \
  fail_api "restore id ${restore_id} does not name ${restore_name}"
[[ "${restore_id}" == "${SPOKE_CLUSTER}/${NAMESPACE}.${restore_name}" ]] || \
  fail_api "restore id ${restore_id}, want ${SPOKE_CLUSTER}/${NAMESPACE}.${restore_name}"
echo "restoring into ${restore_id}"
wait_ready "${restore_id}"
wait_instance "${restore_name}"
echo "${restore_id} is ready"

# The update changes nothing about the pin, so this proves the mechanism and
# not a move across releases: the same chart goes through the same upgrade, and
# the databases made before it have to come out Ready.
step "update the environment in place"
wait_work_finished
lab_issue_join "${UI_PORT}" "${api_cookies}" "${SPOKE_CLUSTER}" "${tmp}/update-join.json" \
  || fail_step "could not mint a token for the update"
update_command="$(lab_update_command "$(lab_json_field "$(cat "${tmp}/update-join.json")" command)")" \
  || fail_step "the hub returned no command to update with"
lab_run_join "${update_command}" "${SPOKE_CLUSTER}" "${REPO_ROOT}/charts/pds" "${STATE_DIR:-${REPO_ROOT}/.local}" \
  || fail_step "the update command failed"
lab_wait_connected "${UI_PORT}" "${api_cookies}" "${SPOKE_CLUSTER}" \
  || fail_step "environment ${SPOKE_CLUSTER} did not come back Connected after the update"
wait_ready "${db_id}"
wait_ready "${restore_id}"
wait_instance "${db_name}"
wait_instance "${restore_name}"
echo "${db_id} and ${restore_id} are Ready after the update"

step "delete databases"
delete_ids=("${db_id}" "${restore_id}")
delete_names=("${db_name}" "${restore_name}")
for id in "${delete_ids[@]}"; do
  api_call DELETE "/api/databases/${id}"
  code="${API_CODE}"
  require_code "${code}" 204 "DELETE ${id}"
  echo "deleted ${id}"
done
for name in "${delete_names[@]}"; do
  wait_git_absent "${name}"
done
wait_gone "${delete_names[@]}"
if [[ "${ENGINE}" == "postgresql" ]]; then
  for name in "${delete_names[@]}"; do
    wait_secrets_gone "${name}"
  done
fi
# The hand-made database was deleted above and must still be gone.
wait_stay_gone "${delete_names[@]}" ${hand_name:+"${hand_name}"}
echo "connected environment ${SPOKE_CLUSTER} has no smoke Instances left"

step "summary"
echo "hub:       kind-${HUB_CLUSTER}"
echo "cluster:   kind-${SPOKE_CLUSTER}"
echo "created:   ${db_id}"
echo "handoff:   ${hand_id:-skipped for ${ENGINE_LABEL}}"
echo "restored:  ${restore_id}"
echo "updated:   ${SPOKE_CLUSTER} in place, databases still Ready"
echo "backup:    ${backup_id}"
echo "alert:     ${alert_summary}"
echo "hub smoke passed"
