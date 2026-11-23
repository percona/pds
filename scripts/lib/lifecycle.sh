#!/usr/bin/env bash
#
# Helpers for the OpenEverest lifecycle contract the nightly hub smoke runs.
# Sourced by scripts/hub-smoke.sh. Pure functions here are covered by
# scripts/lib/lifecycle_test.sh with fixtures, not a cluster.
#
# shellcheck shell=bash

LIFECYCLE_MYSQL_PROVIDER="${LIFECYCLE_MYSQL_PROVIDER:-percona-xtradb-cluster}"
LIFECYCLE_INSTANCE_GVK="${LIFECYCLE_INSTANCE_GVK:-instance.core.openeverest.io}"
LIFECYCLE_BACKUP_GVK="${LIFECYCLE_BACKUP_GVK:-backup.backup.openeverest.io}"
LIFECYCLE_RESTORE_GVK="${LIFECYCLE_RESTORE_GVK:-restore.backup.openeverest.io}"
LIFECYCLE_UNMODELED_PARAM="${LIFECYCLE_UNMODELED_PARAM:-restoreWaitTimeout}"
LIFECYCLE_UNMODELED_VALUE="${LIFECYCLE_UNMODELED_VALUE:-13m}"

LIFECYCLE_POSTGRESQL_PROVIDER="${LIFECYCLE_POSTGRESQL_PROVIDER:-provider-percona-postgresql}"

# The engines the lifecycle runs. Each function that takes an engine fails on
# any other name rather than guessing.
lifecycle_provider() {
  case "$1" in
    mysql) printf '%s' "${LIFECYCLE_MYSQL_PROVIDER}" ;;
    postgresql) printf '%s' "${LIFECYCLE_POSTGRESQL_PROVIDER}" ;;
    *)
      echo "Unknown engine '$1', want mysql or postgresql" >&2
      return 1
      ;;
  esac
}

lifecycle_preset() {
  lifecycle_provider "$1" >/dev/null || return 1
  printf 'pds-%s-dev' "$1"
}

lifecycle_engine_label() {
  case "$1" in
    mysql) printf 'MySQL' ;;
    postgresql) printf 'PostgreSQL' ;;
    *)
      echo "Unknown engine '$1', want mysql or postgresql" >&2
      return 1
      ;;
  esac
}

# The alert step locks the MySQL monitoring account from inside the pxc
# container, so only MySQL has it.
lifecycle_has_alert_step() {
  [[ "$1" == mysql ]]
}

# The unmodeled field is a MySQL provider parameter, so the step that keeps it
# across a scale runs for MySQL only.
lifecycle_has_unmodeled_step() {
  [[ "$1" == mysql ]]
}

lifecycle_create_body() {
  local name="$1"
  local environment="${2:-}"
  local engine="${3:-mysql}"
  lifecycle_provider "${engine}" >/dev/null || return 1
  if [[ -n "${environment}" ]]; then
    printf '{"name":"%s","engine":"%s","profile":"dev","environment":"%s"}' "${name}" "${engine}" "${environment}"
    return 0
  fi
  printf '{"name":"%s","engine":"%s","profile":"dev"}' "${name}" "${engine}"
}

# The Instance a hand-made database is: the preset's spec with one extra
# provider parameter, managed by pds, so the hub only learns of it from a
# heartbeat.
lifecycle_hand_instance() {
  local preset_json="$1" name="$2" namespace="$3" param="$4" value="$5"
  printf '%s' "${preset_json}" | python3 -c 'import json,sys
preset=json.load(sys.stdin)
spec=preset.get("spec")
if not isinstance(spec, dict):
    sys.exit("preset has no spec")
params=spec.get("parameters")
if not isinstance(params, dict):
    params={}
params[sys.argv[3]]=sys.argv[4]
spec["parameters"]=params
doc={
  "apiVersion": preset.get("apiVersion") or "core.openeverest.io/v1alpha1",
  "kind": "Instance",
  "metadata": {
    "name": sys.argv[1],
    "namespace": sys.argv[2],
    "labels": {"app.kubernetes.io/managed-by": "pds"},
  },
  "spec": spec,
}
json.dump(doc, sys.stdout)
sys.stdout.write("\n")
' "${name}" "${namespace}" "${param}" "${value}"
}

lifecycle_scale_body() {
  local cpu="$1" replicas="$2"
  printf '{"cpu":%s,"replicas":%s}' "${cpu}" "${replicas}"
}

lifecycle_restore_body() {
  local backup_id="$1"
  local name="${2:-}"
  if [[ -n "${name}" ]]; then
    printf '{"backupId":"%s","name":"%s"}' "${backup_id}" "${name}"
    return 0
  fi
  printf '{"backupId":"%s"}' "${backup_id}"
}

lifecycle_unmodeled_patch_body() {
  local value="${1:-${LIFECYCLE_UNMODELED_VALUE}}"
  printf '{"spec":{"parameters":{"%s":"%s"}}}' "${LIFECYCLE_UNMODELED_PARAM}" "${value}"
}

# A database id is environment/namespace.name. The name is the segment after the dot.
lifecycle_name_from_id() {
  local id="$1"
  local environment rest namespace name
  if [[ "${id}" != */* ]]; then
    echo "Database id ${id} is not environment/namespace.name" >&2
    return 1
  fi
  environment="${id%%/*}"
  rest="${id#*/}"
  namespace="${rest%%.*}"
  name="${rest#*.}"
  if [[ -z "${environment}" || -z "${rest}" || "${rest}" == */* || "${rest}" != *.* || -z "${namespace}" || -z "${name}" || "${name}" == *.* ]]; then
    echo "Database id ${id} is not environment/namespace.name" >&2
    return 1
  fi
  printf '%s' "${name}"
}

lifecycle_string_field() {
  local json="$1" key="$2"
  printf '%s' "${json}" | python3 -c 'import json,sys
data=json.loads(sys.stdin.read())
key=sys.argv[1]
value=data.get(key, "")
if value is None or value == "":
    sys.exit(1)
print(value)
' "${key}"
}

lifecycle_instance_cpu() {
  printf '%s' "$1" | python3 -c 'import json,sys
spec=json.loads(sys.stdin.read()).get("spec", {})
eng=(spec.get("components") or {}).get("engine") or {}
limits=((eng.get("resources") or {}).get("limits") or {})
cpu=limits.get("cpu")
if cpu is None or cpu == "":
    sys.exit(1)
print(cpu)
'
}

lifecycle_instance_replicas() {
  printf '%s' "$1" | python3 -c 'import json,sys
spec=json.loads(sys.stdin.read()).get("spec", {})
eng=(spec.get("components") or {}).get("engine") or {}
replicas=eng.get("replicas")
if replicas is None or replicas == "":
    sys.exit(1)
print(replicas)
'
}

lifecycle_instance_provider() {
  printf '%s' "$1" | python3 -c 'import json,sys
spec=json.loads(sys.stdin.read()).get("spec", {})
name=((spec.get("providerRef") or {}).get("name") or "")
if not name:
    sys.exit(1)
print(name)
'
}

lifecycle_instance_profile() {
  printf '%s' "$1" | python3 -c 'import json,sys
labels=(json.loads(sys.stdin.read()).get("metadata") or {}).get("labels") or {}
print(labels.get("pds.percona.com/profile", ""))
'
}

lifecycle_instance_parameter() {
  local json="$1" key="$2"
  printf '%s' "${json}" | python3 -c 'import json,sys
spec=json.loads(sys.stdin.read()).get("spec", {})
params=spec.get("parameters") or {}
value=params.get(sys.argv[1], "")
if value is None or value == "":
    sys.exit(1)
print(value)
' "${key}"
}

lifecycle_assert_instance_spec() {
  local json="$1" want_cpu="$2" want_replicas="$3" want_provider="$4"
  local got_cpu got_replicas got_provider
  got_cpu="$(lifecycle_instance_cpu "${json}")" || return 1
  got_replicas="$(lifecycle_instance_replicas "${json}")" || return 1
  got_provider="$(lifecycle_instance_provider "${json}")" || return 1
  [[ "${got_cpu}" == "${want_cpu}" ]] || {
    echo "Instance cpu ${got_cpu}, want ${want_cpu}" >&2
    return 1
  }
  [[ "${got_replicas}" == "${want_replicas}" ]] || {
    echo "Instance replicas ${got_replicas}, want ${want_replicas}" >&2
    return 1
  }
  [[ "${got_provider}" == "${want_provider}" ]] || {
    echo "Instance provider ${got_provider}, want ${want_provider}" >&2
    return 1
  }
}

lifecycle_assert_instance_parameter() {
  local json="$1" key="$2" want="$3"
  local got
  got="$(lifecycle_instance_parameter "${json}" "${key}")" || {
    echo "Instance spec.parameters.${key} is missing" >&2
    return 1
  }
  [[ "${got}" == "${want}" ]] || {
    echo "Instance spec.parameters.${key} ${got}, want ${want}" >&2
    return 1
  }
}

lifecycle_backup_status() {
  local json="$1" backup_id="$2"
  printf '%s' "${json}" | python3 -c 'import json,sys
data=json.loads(sys.stdin.read())
want=sys.argv[1]
for backup in data.get("backups") or []:
    if backup.get("id") == want:
        status=backup.get("status") or ""
        if not status:
            sys.exit(1)
        print(status)
        break
else:
    sys.exit(1)
' "${backup_id}"
}

# lifecycle_unfinished_work reads a kubectl list of Instances, Backups and
# Restores and prints one line for each that is not done: a database in any
# phase but Ready, and a Backup or Restore that is Pending, Running, Deleting or
# has no state yet. It mirrors what the join script's --update refuses to run
# beside, so a caller that waits for it to print nothing is not refused.
lifecycle_unfinished_work() {
  printf '%s' "$1" | python3 -c 'import json,sys
for item in json.loads(sys.stdin.read()).get("items") or []:
    name=(item.get("metadata") or {}).get("name") or "unnamed"
    status=item.get("status") or {}
    kind=item.get("kind") or "object"
    if kind == "Instance":
        phase=status.get("phase") or ""
        if phase != "Ready":
            print("%s %s (%s)" % (kind, name, phase or "no phase yet"))
    else:
        state=status.get("state") or ""
        if state in ("", "Pending", "Running", "Deleting"):
            print("%s %s (%s)" % (kind, name, state or "no state yet"))
'
}

lifecycle_backup_id() {
  printf '%s' "$1" | python3 -c 'import json,sys
data=json.loads(sys.stdin.read())
backups=data.get("backups") or []
if not backups:
    sys.exit(1)
ident=backups[0].get("id") or ""
if not ident:
    sys.exit(1)
print(ident)
'
}

# Succeeds when GET /api/alerts lists the rule firing for the database id.
lifecycle_alert_firing() {
  local json="$1" rule="$2" db_id="$3"
  printf '%s' "${json}" | python3 -c 'import json,sys
try:
    data=json.loads(sys.stdin.read())
except ValueError:
    sys.exit(1)
for alert in data.get("alerts") or []:
    if alert.get("rule") == sys.argv[1] and alert.get("databaseId") == sys.argv[2]:
        sys.exit(0)
sys.exit(1)
' "${rule}" "${db_id}"
}

# Succeeds when Grafana's rule list, as answered by its Prometheus-compatible
# rules route, holds exactly the wanted number of PDS rules (the ones that carry
# a pds_rule label) and every one reports health ok with no lastError. A rule
# with a wrong expression or one Grafana refused to load otherwise reads as no
# data, which the rules treat as healthy.
lifecycle_alert_rules_healthy() {
  local json="$1" want="$2"
  printf '%s' "${json}" | python3 -c 'import json,sys
try:
    data=json.loads(sys.stdin.read())
except ValueError:
    sys.exit(1)
want=int(sys.argv[1])
found=0
for group in (data.get("data") or {}).get("groups") or []:
    for rule in group.get("rules") or []:
        if "pds_rule" not in (rule.get("labels") or {}):
            continue
        found += 1
        if rule.get("health") != "ok" or rule.get("lastError"):
            print("%s: health %s, error %s" % (rule.get("name"), rule.get("health"), rule.get("lastError") or "none"), file=sys.stderr)
            sys.exit(1)
if found != want:
    print("found %d PDS rules, want %d" % (found, want), file=sys.stderr)
    sys.exit(1)
' "${want}"
}

# Prints the state GET /api/alerts reports for one environment.
lifecycle_environment_state() {
  local json="$1" environment="$2"
  printf '%s' "${json}" | python3 -c 'import json,sys
try:
    data=json.loads(sys.stdin.read())
except ValueError:
    sys.exit(1)
for env in data.get("environments") or []:
    if env.get("name") == sys.argv[1] and env.get("state"):
        print(env["state"])
        sys.exit(0)
sys.exit(1)
' "${environment}"
}

# Succeeds when GET /api/databases/<id>/metrics?metrics=connections shows the
# database's own service is being scraped: the response is not pending and the
# connections series has a number as its newest point. Only the engine's
# exporter produces that series: mysqld_exporter's threads_connected for MySQL,
# postgres_exporter's pg_stat_activity_count for PostgreSQL, in the same answer.
# The cpu probe is not enough, because node series, and HAProxy's for MySQL,
# answer it before pmm-client has added the database's service.
lifecycle_metrics_flowing() {
  local json="$1"
  printf '%s' "${json}" | python3 -c 'import json,sys
try:
    data=json.loads(sys.stdin.read())
except ValueError:
    sys.exit(1)
if not isinstance(data, dict) or data.get("pending") is not False:
    sys.exit(1)
for m in data.get("metrics") or []:
    if not isinstance(m, dict) or m.get("kind") != "connections":
        continue
    points = m.get("points") or []
    if points and isinstance(points[-1], dict):
        v = points[-1].get("v")
        if isinstance(v, (int, float)) and not isinstance(v, bool):
            sys.exit(0)
sys.exit(1)
'
}

# Succeeds when the environments list says the named environment has
# monitoring, so a database created now gets it.
lifecycle_environment_monitored() {
  local json="$1" name="$2"
  printf '%s' "${json}" | python3 -c 'import json,sys
try:
    data=json.loads(sys.stdin.read())
except ValueError:
    sys.exit(1)
rows = data.get("environments") if isinstance(data, dict) else None
for row in rows or []:
    if isinstance(row, dict) and row.get("name") == sys.argv[1]:
        sys.exit(0 if row.get("monitoring") is True else 1)
sys.exit(1)
' "${name}"
}

lifecycle_ready_outcome() {
  case "$1" in
    ready) printf 'ready' ;;
    failed) printf 'failed' ;;
    *) printf 'wait' ;;
  esac
}

lifecycle_backup_outcome() {
  case "$1" in
    succeeded) printf 'succeeded' ;;
    failed) printf 'failed' ;;
    *) printf 'wait' ;;
  esac
}

# After scale or restore the API still says ready until the provider moves.
# Callers set seen_non_ready once a poll reports anything else.
lifecycle_return_ready_outcome() {
  local status="$1" seen_non_ready="$2"
  case "$(lifecycle_ready_outcome "${status}")" in
    failed) printf 'failed' ;;
    ready)
      if [[ "${seen_non_ready}" == true ]]; then
        printf 'ready'
      else
        printf 'wait'
      fi
      ;;
    *) printf 'wait' ;;
  esac
}

lifecycle_backup_poll_outcome() {
  local status="${1:-}"
  if [[ -z "${status}" ]]; then
    printf 'wait'
    return 0
  fi
  lifecycle_backup_outcome "${status}"
}

lifecycle_gone_outcome() {
  local code="$1" instance_state="$2"
  case "${code}" in
    404)
      if [[ "${instance_state}" == "missing" ]]; then
        printf 'gone'
      else
        printf 'wait'
      fi
      ;;
    200)
      printf 'wait'
      ;;
    *)
      printf 'error'
      ;;
  esac
}

lifecycle_is_skipped() {
  [[ "$1" == skipped* ]]
}

lifecycle_require() {
  local required="$1" status="$2" what="$3"
  if [[ "${required}" == true ]] && lifecycle_is_skipped "${status}"; then
    echo "${what} was skipped but --require-${what} was passed" >&2
    return 1
  fi
}

# Restore is implied by a succeeded backup. There is no --require-restore flag.
lifecycle_require_restore() {
  local backup_status="$1" restore_status="$2"
  if [[ "${backup_status}" == succeeded* ]] && lifecycle_is_skipped "${restore_status}"; then
    echo "backup succeeded but restore was skipped" >&2
    return 1
  fi
}

# JSON and assignment keys match after stripping separators so api_key,
# api-key, apiKey, and x-sn-apikey all redact.
lifecycle_redact() {
  python3 -c 'import json,re,sys
text=sys.argv[1]
def sensitive_key(key):
    n=re.sub(r"[^a-z0-9]+", "", key.lower())
    if n in {"uri","dsn","auth"}:
        return True
    return any(p in n for p in (
        "password","passwd","pwd","secret","token","apikey","authorization",
        "credential","privatekey","accesskey","connectionstring","connectionuri",
        "connuri","databaseurl","dburl","databaseuri"))
assign=r"(?i)([A-Za-z0-9_.\-]*(?:password|passwd|pwd|secret|token|apikey|api[_-]?key|credential|authorization)[A-Za-z0-9_.\-]*\"?\s*[:=]\s*\"?)[^\"\s]+"
def scrub(value):
    if isinstance(value, dict):
        out={}
        for key, item in value.items():
            if sensitive_key(key):
                out[key]="[redacted]"
            else:
                out[key]=scrub(item)
        return out
    if isinstance(value, list):
        return [scrub(item) for item in value]
    if isinstance(value, str):
        value=re.sub(r"(?i)(bearer\s+)[A-Za-z0-9._\-+=/]+", r"\1[redacted]", value)
        value=re.sub(assign, r"\1[redacted]", value)
        value=re.sub(r"mysql://[^\s\"'\'']+", "mysql://[redacted]", value)
        value=re.sub(r"postgres(?:ql)?://[^\s\"'\'']+", "postgres://[redacted]", value)
        return value
    return value
try:
    print(json.dumps(scrub(json.loads(text))))
except Exception:
    out=text
    out=re.sub(r"(?i)(bearer\s+)[A-Za-z0-9._\-+=/]+", r"\1[redacted]", out)
    out=re.sub(assign, r"\1[redacted]", out)
    out=re.sub(r"mysql://[^\s\"'\'']+", "mysql://[redacted]", out)
    out=re.sub(r"postgres(?:ql)?://[^\s\"'\'']+", "postgres://[redacted]", out)
    sys.stdout.write(out)
' "$1"
}

# The label selector for the pods of the named databases. A database's pods
# carry its name in this label, and so does the pgBackRest job that seeds a
# PostgreSQL restore copy.
lifecycle_instance_selector() {
  local IFS=,
  printf 'app.kubernetes.io/instance in (%s)' "$*"
}

lifecycle_kube() {
  if declare -F kube >/dev/null; then
    kube "$@"
  else
    kubectl "$@"
  fi
}

lifecycle_dump_cmd() {
  lifecycle_kube "$@" 2>&1 | lifecycle_redact_stream || true
}

lifecycle_redact_stream() {
  python3 -c 'import re,sys
text=sys.stdin.read()
assign=r"(?i)([A-Za-z0-9_.\-]*(?:password|passwd|pwd|secret|token|apikey|api[_-]?key|credential|authorization)[A-Za-z0-9_.\-]*\"?\s*[:=]\s*\"?)[^\"\s]+"
text=re.sub(r"(?i)(bearer\s+)[A-Za-z0-9._\-+=/]+", r"\1[redacted]", text)
text=re.sub(assign, r"\1[redacted]", text)
text=re.sub(r"mysql://[^\s\"'\'']+", "mysql://[redacted]", text)
text=re.sub(r"postgres(?:ql)?://[^\s\"'\'']+", "postgres://[redacted]", text)
sys.stdout.write(text)
'
}

# Cluster state for a failed lifecycle step. Reads pods, OpenEverest objects,
# provider logs, and events. Never reads Secrets.
lifecycle_dump() {
  local ns="${NAMESPACE:-pds}"
  local everest_ns="${EVEREST_NAMESPACE:-everest-system}"
  local adapter_ns="${RELEASE_NS:-${everest_ns}}"
  local release="${RELEASE:-pds}"

  # Every kind node keeps its own copy of each image, so a full disk shows up as
  # failed layer extraction long before anything else.
  echo "disk:"
  df -h / 2>&1 || true
  docker system df 2>&1 || true
  local node
  while IFS= read -r node; do
    [[ -z "${node}" ]] && continue
    echo "${node}:"
    docker exec "${node}" df -h /var 2>&1 || true
  done < <(docker ps --filter "label=io.x-k8s.kind.cluster=${CLUSTER:-pds}" --format '{{.Names}}' 2>/dev/null || true)

  echo "Percona Database Service adapter pods:"
  lifecycle_dump_cmd get pods -n "${adapter_ns}" -o wide
  lifecycle_dump_cmd logs -n "${adapter_ns}" "deployment/${release}-adapter" --tail=200
  if [[ "${adapter_ns}" != "${ns}" ]]; then
    lifecycle_dump_cmd get pods -n "${ns}" -o wide
    lifecycle_dump_cmd logs -n "${ns}" "deployment/${release}-adapter" --tail=200
  fi
  echo "OpenEverest:"
  lifecycle_dump_cmd get pods -n "${everest_ns}" -o wide
  lifecycle_dump_cmd logs -n "${everest_ns}" deployment/everest-server --tail=200
  lifecycle_dump_cmd logs -n "${everest_ns}" deployment/everest-operator --tail=200
  lifecycle_dump_cmd logs -n "${everest_ns}" deployment/everest-controller --tail=200
  echo "database pods:"
  lifecycle_dump_cmd describe pods -n "${ns}"
  local pod
  while IFS= read -r pod; do
    [[ -z "${pod}" ]] && continue
    lifecycle_dump_cmd logs -n "${ns}" "${pod}" --all-containers --prefix --tail=80
  done < <(lifecycle_kube -n "${ns}" get pods -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
  echo "OpenEverest objects:"
  lifecycle_dump_cmd get "${LIFECYCLE_INSTANCE_GVK},${LIFECYCLE_BACKUP_GVK},${LIFECYCLE_RESTORE_GVK}" -n "${ns}" -o wide
  lifecycle_dump_cmd get "${LIFECYCLE_INSTANCE_GVK},${LIFECYCLE_BACKUP_GVK},${LIFECYCLE_RESTORE_GVK}" -n "${ns}" -o yaml
  echo "provider:"
  local deploy
  while IFS= read -r deploy; do
    [[ -z "${deploy}" ]] && continue
    case "${deploy}" in
      *pxc*|*xtradb*|*postgresql*|*pg-operator*|*mongodb*|*psmdb*)
        lifecycle_dump_cmd logs -n "${everest_ns}" "deployment/${deploy}" --tail=200
        ;;
    esac
  done < <(lifecycle_kube -n "${everest_ns}" get deploy -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
  echo "Kubernetes events (last 80):"
  # Full event history floods the CI log and gets truncated first.
  lifecycle_kube get events -A --sort-by=.lastTimestamp 2>&1 | tail -n 80 | lifecycle_redact_stream || true
  echo "all pods:"
  lifecycle_dump_cmd get pods -A -o wide
}
