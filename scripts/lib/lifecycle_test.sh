#!/usr/bin/env bash
#
# Unit tests for scripts/lib/lifecycle.sh. No cluster: fixtures and fake kubectl.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=lifecycle.sh
source "${REPO_ROOT}/scripts/lib/lifecycle.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

command -v python3 >/dev/null || fail "python3 is required"

# --- request bodies ---
got="$(lifecycle_create_body smoke-db)"
printf '%s' "${got}" | grep -q '"name":"smoke-db"' || fail "create name: ${got}"
printf '%s' "${got}" | grep -q '"engine":"mysql"' || fail "create engine: ${got}"
printf '%s' "${got}" | grep -q '"profile":"dev"' || fail "create profile: ${got}"
pass create-body

got="$(lifecycle_scale_body 2 3)"
printf '%s' "${got}" | grep -q '"cpu":2' || fail "scale cpu: ${got}"
printf '%s' "${got}" | grep -q '"replicas":3' || fail "scale replicas: ${got}"
pass scale-body

got="$(lifecycle_restore_body orders-dev-20260824091500)"
printf '%s' "${got}" | grep -q '"backupId":"orders-dev-20260824091500"' || fail "restore body: ${got}"
pass restore-body

got="$(lifecycle_unmodeled_patch_body 13m)"
printf '%s' "${got}" | grep -q '"restoreWaitTimeout":"13m"' || fail "unmodeled patch: ${got}"
printf '%s' "${got}" | grep -q '"parameters"' || fail "unmodeled parameters: ${got}"
pass unmodeled-patch

# --- ids ---
# A database id is environment/namespace.name. The name is the segment after the dot.
got="$(lifecycle_name_from_id spoke-a/pds.smoke-1)"
[[ "${got}" == "smoke-1" ]] || fail "name from environment id: ${got}"
got="$(lifecycle_name_from_id 'spoke-a/pds.orders-dev')"
[[ "${got}" == "orders-dev" ]] || fail "name from qualified orders id: ${got}"
for bad in \
  'pds.smoke-1' \
  'not-an-id' \
  '/pds.smoke-1' \
  'spoke-a/.smoke-1' \
  'spoke-a/pds.' \
  'spoke-a/' \
  'spoke-a/pds.smoke-1/extra' \
  'a/b/pds.smoke-1' \
  'spoke-a/pds.smoke.extra' \
  'pds.smoke.extra'; do
  if lifecycle_name_from_id "${bad}" >/dev/null 2>&1; then
    fail "name from id should reject ${bad}"
  fi
done
pass name-from-id

got="$(lifecycle_create_body smoke-db spoke-a)"
printf '%s' "${got}" | grep -q '"name":"smoke-db"' || fail "hub create name: ${got}"
printf '%s' "${got}" | grep -q '"environment":"spoke-a"' || fail "hub create environment: ${got}"
printf '%s' "${got}" | grep -q '"engine":"mysql"' || fail "hub create engine: ${got}"
printf '%s' "${got}" | grep -q '"profile":"dev"' || fail "hub create profile: ${got}"
pass create-body-environment

got="$(lifecycle_create_body smoke-db spoke-a postgresql)"
printf '%s' "${got}" | grep -q '"engine":"postgresql"' || fail "postgresql create engine: ${got}"
printf '%s' "${got}" | grep -q '"environment":"spoke-a"' || fail "postgresql create environment: ${got}"
got="$(lifecycle_create_body smoke-db "" postgresql)"
printf '%s' "${got}" | grep -q '"engine":"postgresql"' || fail "postgresql create without environment: ${got}"
if printf '%s' "${got}" | grep -q 'environment'; then fail "create without environment names one: ${got}"; fi
pass create-body-engine

# --- hand-made Instance ---
preset='{"apiVersion":"core.openeverest.io/v1alpha1","spec":{"providerRef":{"name":"p"},"parameters":{"keep":"1"}}}'
got="$(lifecycle_hand_instance "${preset}" hand-1 pds restoreWaitTimeout 13m)" || fail "hand instance did not build"
printf '%s' "${got}" | python3 -c 'import json,sys
d=json.load(sys.stdin)
assert d["kind"]=="Instance" and d["metadata"]["name"]=="hand-1" and d["metadata"]["namespace"]=="pds"
assert d["metadata"]["labels"]["app.kubernetes.io/managed-by"]=="pds"
assert d["spec"]["parameters"]=={"keep":"1","restoreWaitTimeout":"13m"}
assert d["spec"]["providerRef"]["name"]=="p"
' || fail "hand instance shape: ${got}"
if lifecycle_hand_instance '{}' hand-1 pds a b >/dev/null 2>&1; then fail "hand instance should refuse a preset with no spec"; fi
pass hand-instance

# --- engines ---
[[ "$(lifecycle_provider mysql)" == "percona-xtradb-cluster" ]] || fail "mysql provider"
[[ "$(lifecycle_provider postgresql)" == "provider-percona-postgresql" ]] || fail "postgresql provider"
[[ "$(lifecycle_preset mysql)" == "pds-mysql-dev" ]] || fail "mysql preset"
[[ "$(lifecycle_preset postgresql)" == "pds-postgresql-dev" ]] || fail "postgresql preset"
[[ "$(lifecycle_engine_label mysql)" == "MySQL" ]] || fail "mysql label"
[[ "$(lifecycle_engine_label postgresql)" == "PostgreSQL" ]] || fail "postgresql label"
lifecycle_has_alert_step mysql || fail "mysql runs the alert step"
lifecycle_has_unmodeled_step mysql || fail "mysql runs the unmodeled step"
if lifecycle_has_alert_step postgresql; then fail "postgresql has no alert step"; fi
if lifecycle_has_unmodeled_step postgresql; then fail "postgresql has no unmodeled step"; fi
for bad in "" mongodb MySQL; do
  if lifecycle_provider "${bad}" >/dev/null 2>&1; then fail "provider should reject '${bad}'"; fi
done
pass engines

got="$(lifecycle_restore_body orders-dev-20260824091500 orders-restore-1)"
printf '%s' "${got}" | grep -q '"backupId":"orders-dev-20260824091500"' || fail "hub restore backup: ${got}"
printf '%s' "${got}" | grep -q '"name":"orders-restore-1"' || fail "hub restore name: ${got}"
pass restore-body-name

# --- OpenEverest Instance spec ---
instance_json='{
  "apiVersion": "core.openeverest.io/v1alpha1",
  "kind": "Instance",
  "metadata": {
    "name": "smoke-1",
    "namespace": "pds",
    "labels": {
      "app.kubernetes.io/managed-by": "pds",
      "pds.percona.com/profile": "dev"
    }
  },
  "spec": {
    "providerRef": { "name": "percona-xtradb-cluster" },
    "parameters": { "restoreWaitTimeout": "13m" },
    "components": {
      "engine": {
        "replicas": 3,
        "resources": { "limits": { "cpu": "2", "memory": "2Gi" } },
        "storage": { "size": "20Gi" }
      }
    }
  }
}'

got="$(lifecycle_instance_cpu "${instance_json}")"
[[ "${got}" == "2" ]] || fail "instance cpu: ${got}"
got="$(lifecycle_instance_replicas "${instance_json}")"
[[ "${got}" == "3" ]] || fail "instance replicas: ${got}"
got="$(lifecycle_instance_provider "${instance_json}")"
[[ "${got}" == "percona-xtradb-cluster" ]] || fail "instance provider: ${got}"
got="$(lifecycle_instance_profile "${instance_json}")"
[[ "${got}" == "dev" ]] || fail "instance profile: ${got}"
got="$(lifecycle_instance_parameter "${instance_json}" restoreWaitTimeout)"
[[ "${got}" == "13m" ]] || fail "instance parameter: ${got}"
pass instance-spec

if lifecycle_assert_instance_spec "${instance_json}" 2 3 percona-xtradb-cluster; then
  pass assert-instance-spec
else
  fail "assert instance spec rejected a matching object"
fi
if lifecycle_assert_instance_spec "${instance_json}" 1 3 percona-xtradb-cluster >/dev/null 2>&1; then
  fail "assert instance spec accepted the wrong cpu"
fi
if lifecycle_assert_instance_parameter "${instance_json}" restoreWaitTimeout 13m; then
  pass assert-unmodeled
else
  fail "assert unmodeled rejected a matching parameter"
fi
if lifecycle_assert_instance_parameter "${instance_json}" restoreWaitTimeout 10m >/dev/null 2>&1; then
  fail "assert unmodeled accepted the wrong timeout"
fi
missing_param='{"spec":{"providerRef":{"name":"percona-xtradb-cluster"},"components":{"engine":{"replicas":1,"resources":{"limits":{"cpu":"1"}}}}}}'
if lifecycle_assert_instance_parameter "${missing_param}" restoreWaitTimeout 13m >/dev/null 2>&1; then
  fail "assert unmodeled accepted a missing parameter"
fi
pass assert-unmodeled-missing

# --- backup list ---
backup_json='{"backups":[{"id":"smoke-1-on-demand","status":"succeeded"},{"id":"older","status":"failed"}]}'
got="$(lifecycle_backup_status "${backup_json}" smoke-1-on-demand)"
[[ "${got}" == "succeeded" ]] || fail "backup status: ${got}"
got="$(lifecycle_backup_id "${backup_json}")"
[[ "${got}" == "smoke-1-on-demand" ]] || fail "backup id: ${got}"
pass backup-list

got="$(lifecycle_string_field '{"id":"smoke-1-on-demand","status":"pending"}' id)"
[[ "${got}" == "smoke-1-on-demand" ]] || fail "string field id: ${got}"
got="$(lifecycle_string_field '{"id":"smoke-1-on-demand","status":"pending"}' status)"
[[ "${got}" == "pending" ]] || fail "string field status: ${got}"
pass string-field

# --- wait outcomes ---
[[ "$(lifecycle_ready_outcome ready)" == "ready" ]] || fail "ready outcome"
[[ "$(lifecycle_ready_outcome restoring)" == "wait" ]] || fail "restoring should wait"
[[ "$(lifecycle_ready_outcome updating)" == "wait" ]] || fail "updating should wait"
[[ "$(lifecycle_ready_outcome failed)" == "failed" ]] || fail "failed outcome"
[[ "$(lifecycle_backup_outcome succeeded)" == "succeeded" ]] || fail "backup succeeded"
[[ "$(lifecycle_backup_outcome running)" == "wait" ]] || fail "backup running"
[[ "$(lifecycle_backup_outcome failed)" == "failed" ]] || fail "backup failed"
[[ "$(lifecycle_gone_outcome 404 missing)" == "gone" ]] || fail "gone 404"
[[ "$(lifecycle_gone_outcome 200 found)" == "wait" ]] || fail "still present"
[[ "$(lifecycle_gone_outcome 500 found)" == "error" ]] || fail "unexpected get"
[[ "$(lifecycle_return_ready_outcome ready false)" == "wait" ]] || fail "still the pre-write ready"
[[ "$(lifecycle_return_ready_outcome ready true)" == "ready" ]] || fail "returned to ready"
[[ "$(lifecycle_return_ready_outcome restoring false)" == "wait" ]] || fail "restoring should wait"
[[ "$(lifecycle_return_ready_outcome failed false)" == "failed" ]] || fail "failed after write"
[[ "$(lifecycle_backup_poll_outcome "")" == "wait" ]] || fail "missing backup should wait"
[[ "$(lifecycle_backup_poll_outcome succeeded)" == "succeeded" ]] || fail "backup poll succeeded"
[[ "$(lifecycle_backup_poll_outcome failed)" == "failed" ]] || fail "backup poll failed"
pass wait-outcomes

# --- skipped steps fail the nightly ---
if lifecycle_require true "skipped, no OpenEverest" lifecycle >/dev/null 2>&1; then
  fail "require-lifecycle should fail a skipped create"
fi
if ! lifecycle_require true "created pds.smoke-1" lifecycle >/dev/null 2>&1; then
  fail "require-lifecycle should pass a completed create"
fi
if lifecycle_require true "skipped, no BackupStorage" backup >/dev/null 2>&1; then
  fail "require-backup should fail a skipped backup"
fi
if ! lifecycle_require false "skipped, --no-lifecycle" lifecycle >/dev/null 2>&1; then
  fail "require off should allow a skip"
fi
if lifecycle_require_restore "succeeded smoke-1-on-demand" "skipped, no BackupStorage" >/dev/null 2>&1; then
  fail "restore should fail when backup succeeded and restore was skipped"
fi
msg="$(lifecycle_require_restore "succeeded smoke-1-on-demand" "skipped, no BackupStorage" 2>&1 || true)"
printf '%s' "${msg}" | grep -q -- '--require-restore' && fail "restore skip must not mention --require-restore: ${msg}"
printf '%s' "${msg}" | grep -q 'backup succeeded but restore was skipped' || fail "restore skip message: ${msg}"
if ! lifecycle_require_restore "succeeded smoke-1-on-demand" "succeeded smoke-1-on-demand" >/dev/null 2>&1; then
  fail "restore should pass when backup and restore both succeeded"
fi
if ! lifecycle_require_restore "skipped, no BackupStorage" "skipped, no BackupStorage" >/dev/null 2>&1; then
  fail "restore should pass when backup was skipped"
fi
pass require-skipped

# --- credentials stay out of failure output ---
leaky='{"id":"pds.smoke-1","connection":{"uri":"mysql://root:s3cret@smoke-1:3306","password":"s3cret"},"token":"eyJhbGciOiJIUzI1NiJ9.payload"}'
got="$(lifecycle_redact "${leaky}")"
printf '%s' "${got}" | grep -q 's3cret' && fail "redact leaked password: ${got}"
printf '%s' "${got}" | grep -q 'eyJhbGciOiJIUzI1NiJ9' && fail "redact leaked token: ${got}"
printf '%s' "${got}" | grep -q 'mysql://root:' && fail "redact leaked uri: ${got}"
printf '%s' "${got}" | grep -q 'pds.smoke-1' || fail "redact dropped the id: ${got}"
got="$(lifecycle_redact 'Authorization: Bearer eyJhbGciOiJIUz.abc MINIO_ROOT_PASSWORD=minio12345 AWS_SECRET_ACCESS_KEY=wJalr')"
printf '%s' "${got}" | grep -q 'eyJhbGciOiJIUz' && fail "redact leaked bearer: ${got}"
printf '%s' "${got}" | grep -q 'minio12345' && fail "redact leaked minio: ${got}"
printf '%s' "${got}" | grep -q 'wJalr' && fail "redact leaked aws: ${got}"
got="$(lifecycle_redact '{"note":"mysql://root:s3cret@smoke-1:3306"}')"
printf '%s' "${got}" | grep -q 's3cret' && fail "redact leaked url in note: ${got}"
got="$(lifecycle_redact '{"message":"Authorization: Bearer eyJhbGciOiJIUz.abc client_token=tok-99"}')"
printf '%s' "${got}" | grep -q 'eyJhbGciOiJIUz' && fail "redact leaked bearer in json string: ${got}"
printf '%s' "${got}" | grep -q 'tok-99' && fail "redact leaked client_token in json string: ${got}"
got="$(lifecycle_redact '{"id":"pds.smoke-1","client_token":"tok-99","api_key":"k-1234567890","x-sn-apikey":"sn-header"}')"
printf '%s' "${got}" | grep -q 'tok-99' && fail "redact leaked client_token: ${got}"
printf '%s' "${got}" | grep -q 'k-1234567890' && fail "redact leaked api_key: ${got}"
printf '%s' "${got}" | grep -q 'sn-header' && fail "redact leaked x-sn-apikey: ${got}"
printf '%s' "${got}" | grep -q 'pds.smoke-1' || fail "redact dropped the id: ${got}"
got="$(lifecycle_redact 'client_token=tok-99 api-key=k-1234567890 api_key=k-abcdef x-sn-apikey: sn-header')"
printf '%s' "${got}" | grep -q 'tok-99' && fail "redact leaked assignment client_token: ${got}"
printf '%s' "${got}" | grep -q 'k-1234567890' && fail "redact leaked assignment api-key: ${got}"
printf '%s' "${got}" | grep -q 'k-abcdef' && fail "redact leaked assignment api_key: ${got}"
printf '%s' "${got}" | grep -q 'sn-header' && fail "redact leaked assignment x-sn-apikey: ${got}"
got="$(printf '%s\n' 'client_token=tok-99 api_key=k-1234567890 x-sn-apikey: sn-header' | lifecycle_redact_stream)"
printf '%s' "${got}" | grep -q 'tok-99' && fail "stream leaked client_token: ${got}"
printf '%s' "${got}" | grep -q 'k-1234567890' && fail "stream leaked api_key: ${got}"
printf '%s' "${got}" | grep -q 'sn-header' && fail "stream leaked x-sn-apikey: ${got}"
pass redact

# --- diagnostics call the cluster, never secrets ---
FAKE="$(mktemp -d)"
trap 'rm -rf "${FAKE}"' EXIT
export FAKE_LOG="${FAKE}/kubectl.log"
cat >"${FAKE}/kubectl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${FAKE_LOG}"
case "$*" in
  *secret*|*secrets*)
    echo "kubectl must not read secrets during diagnostics" >&2
    exit 1
    ;;
esac
exit 0
EOF
chmod +x "${FAKE}/kubectl"
PATH="${FAKE}:${PATH}"
lifecycle_dump >/dev/null
grep -q 'get pods' "${FAKE_LOG}" || fail "dump skipped pods: $(cat "${FAKE_LOG}")"
grep -q 'instance' "${FAKE_LOG}" || fail "dump skipped instances: $(cat "${FAKE_LOG}")"
grep -q 'backup' "${FAKE_LOG}" || fail "dump skipped backups: $(cat "${FAKE_LOG}")"
grep -q 'events' "${FAKE_LOG}" || fail "dump skipped events: $(cat "${FAKE_LOG}")"
grep -q 'describe pods' "${FAKE_LOG}" || fail "dump skipped describe pods: $(cat "${FAKE_LOG}")"
grep -q -- '-o yaml' "${FAKE_LOG}" || fail "dump skipped object yaml: $(cat "${FAKE_LOG}")"
if grep -qiE 'get secrets? |get secret ' "${FAKE_LOG}"; then
  fail "dump read secrets: $(cat "${FAKE_LOG}")"
fi
pass dump-no-secrets

# A restore copy's pods, its restore job's among them, carry the copy's name,
# so the dump selects them beside the source database's.
got="$(lifecycle_instance_selector hub-1)"
[[ "${got}" == "app.kubernetes.io/instance in (hub-1)" ]] || fail "instance selector for one database: ${got}"
got="$(lifecycle_instance_selector hub-1 rest-1)"
[[ "${got}" == "app.kubernetes.io/instance in (hub-1,rest-1)" ]] || fail "instance selector with a restore copy: ${got}"
pass instance-selector

# --- alerts ---
alerts_json='{"environments":[{"name":"spoke-a","state":"live"},{"name":"spoke-b","state":"off"}],"alerts":[{"rule":"service_down","databaseId":"spoke-a/pds.orders","environment":"spoke-a"},{"rule":"backup_failed","databaseId":"spoke-a/pds.billing","environment":"spoke-a"}],"criticals":1}'
lifecycle_alert_firing "${alerts_json}" service_down spoke-a/pds.orders || fail "alert firing: missed a firing alert"
pass alert-firing
if lifecycle_alert_firing "${alerts_json}" service_down spoke-a/pds.billing; then
  fail "alert firing: matched the wrong rule for a database"
fi
if lifecycle_alert_firing "${alerts_json}" service_down spoke-b/pds.orders; then
  fail "alert firing: matched another environment's database"
fi
if lifecycle_alert_firing '{"environments":[],"alerts":null,"criticals":0}' service_down spoke-a/pds.orders; then
  fail "alert firing: matched a null alert list"
fi
if lifecycle_alert_firing 'not json' service_down spoke-a/pds.orders; then
  fail "alert firing: matched unreadable json"
fi
pass alert-not-firing

got="$(lifecycle_environment_state "${alerts_json}" spoke-a)"
[[ "${got}" == "live" ]] || fail "environment state: ${got}"
got="$(lifecycle_environment_state "${alerts_json}" spoke-b)"
[[ "${got}" == "off" ]] || fail "environment state off: ${got}"
if lifecycle_environment_state "${alerts_json}" spoke-c >/dev/null 2>&1; then
  fail "environment state: answered for an unknown environment"
fi
pass environment-state

metrics_live='{"databaseId":"spoke-a/pds.orders","pending":false,"metrics":[{"kind":"connections","pending":false,"points":[{"t":"2026-10-13T10:00:00Z","v":3},{"t":"2026-10-13T10:00:30Z","v":4}]}]}'
metrics_pending='{"databaseId":"spoke-a/pds.orders","pending":true,"pendingReason":"Monitoring has no target for this database yet.","metrics":[{"kind":"connections","pending":true,"points":[]}]}'
lifecycle_metrics_flowing "${metrics_live}" || fail "metrics flowing: a live reading read as not flowing"
pass metrics-flowing
if lifecycle_metrics_flowing "${metrics_pending}"; then
  fail "metrics flowing: a pending reading read as flowing"
fi
if lifecycle_metrics_flowing '{"databaseId":"spoke-a/pds.orders","metrics":[{"kind":"connections","points":[{"t":"x","v":1}]}]}'; then
  fail "metrics flowing: a response without pending read as flowing"
fi
if lifecycle_metrics_flowing '{"pending":false,"metrics":[]}'; then
  fail "metrics flowing: a response without the connections series read as flowing"
fi
if lifecycle_metrics_flowing '{"pending":false,"metrics":[{"kind":"cpu","points":[{"t":"x","v":1}]}]}'; then
  fail "metrics flowing: a cpu series alone read as flowing"
fi
if lifecycle_metrics_flowing '{"pending":false,"metrics":[{"kind":"connections","points":[]}]}'; then
  fail "metrics flowing: a connections series with no points read as flowing"
fi
if lifecycle_metrics_flowing '{"pending":false,"metrics":[{"kind":"connections","points":[{"t":"x","v":2},{"t":"y","v":null}]}]}'; then
  fail "metrics flowing: a null newest value read as flowing"
fi
if lifecycle_metrics_flowing 'not json'; then
  fail "metrics flowing: unreadable json read as flowing"
fi
pass metrics-not-flowing

# A PostgreSQL database charts connections from postgres_exporter in the same
# answer MySQL's gets. Node series reach monitoring first, so before the
# exporter is added the answer is not pending and connections has no points.
metrics_postgresql_live='{"databaseId":"spoke-a/pds.reports","window":"1h","start":"2026-10-21T09:00:00Z","end":"2026-10-21T10:00:00Z","stepSeconds":300,"pending":false,"metrics":[{"kind":"connections","unit":"count","pending":false,"current":6,"min":5,"max":6,"average":5.5,"trend":"steady","points":[{"t":"2026-10-21T09:50:00Z","v":5},{"t":"2026-10-21T09:55:00Z","v":6}]}]}'
metrics_postgresql_nodes_only='{"databaseId":"spoke-a/pds.reports","window":"1h","start":"2026-10-21T09:00:00Z","end":"2026-10-21T10:00:00Z","stepSeconds":300,"pending":false,"metrics":[{"kind":"connections","unit":"count","pending":false,"current":0,"min":0,"max":0,"average":0,"trend":"steady","points":[]}]}'
lifecycle_metrics_flowing "${metrics_postgresql_live}" \
  || fail "metrics flowing: a PostgreSQL connections reading read as not flowing"
if lifecycle_metrics_flowing "${metrics_postgresql_nodes_only}"; then
  fail "metrics flowing: a PostgreSQL database with no exporter reading read as flowing"
fi
pass metrics-flowing-postgresql

environments_monitored='{"hub":true,"environments":[{"name":"spoke-b","status":"connected","monitoring":false},{"name":"spoke-a","status":"connected","monitoring":true}]}'
lifecycle_environment_monitored "${environments_monitored}" spoke-a || fail "environment monitored: a monitored environment read as unmonitored"
pass environment-monitored
if lifecycle_environment_monitored "${environments_monitored}" spoke-b; then
  fail "environment monitored: monitoring false read as monitored"
fi
if lifecycle_environment_monitored '{"environments":[{"name":"spoke-a","status":"connected"}]}' spoke-a; then
  fail "environment monitored: a row without monitoring read as monitored"
fi
if lifecycle_environment_monitored '{"environments":[{"name":"spoke-a","monitoring":"true"}]}' spoke-a; then
  fail "environment monitored: a string read as monitored"
fi
if lifecycle_environment_monitored "${environments_monitored}" spoke-c; then
  fail "environment monitored: an unlisted environment read as monitored"
fi
if lifecycle_environment_monitored 'not json' spoke-a; then
  fail "environment monitored: unreadable json read as monitored"
fi
pass environment-not-monitored

# --- Grafana's rule list ---
rule_row() { printf '{"name":"%s","health":"%s","lastError":"%s","labels":{"pds_rule":"x"}}' "$1" "$2" "${3:-}"; }
rules_ok="{\"data\":{\"groups\":[{\"name\":\"MySQL\",\"rules\":[$(rule_row a ok),$(rule_row b ok)]},{\"name\":\"Other\",\"rules\":[{\"name\":\"c\",\"health\":\"error\",\"labels\":{}}]}]}}"
lifecycle_alert_rules_healthy "${rules_ok}" 2 2>/dev/null || fail "rules healthy: two ok PDS rules read as unhealthy"
if lifecycle_alert_rules_healthy "${rules_ok}" 3 2>/dev/null; then
  fail "rules healthy: a missing PDS rule read as healthy"
fi
rules_bad="{\"data\":{\"groups\":[{\"name\":\"PostgreSQL\",\"rules\":[$(rule_row a ok),$(rule_row b error 'parse error')]}]}}"
if lifecycle_alert_rules_healthy "${rules_bad}" 2 2>/dev/null; then
  fail "rules healthy: a rule with health error read as healthy"
fi
rules_lasterror="{\"data\":{\"groups\":[{\"name\":\"PostgreSQL\",\"rules\":[$(rule_row a ok 'bad series')]}]}}"
if lifecycle_alert_rules_healthy "${rules_lasterror}" 1 2>/dev/null; then
  fail "rules healthy: a rule with a lastError read as healthy"
fi
if lifecycle_alert_rules_healthy 'not json' 1 2>/dev/null; then
  fail "rules healthy: unreadable json read as healthy"
fi
pass alert-rules-healthy

# --- unfinished work in the database namespace ---
unfinished_list='{"items":[
 {"kind":"Instance","metadata":{"name":"smoke-1"},"status":{"phase":"Ready"}},
 {"kind":"Instance","metadata":{"name":"rest-1"},"status":{"phase":"Initializing"}},
 {"kind":"Instance","metadata":{"name":"susp-1"},"status":{"phase":"Suspended"}},
 {"kind":"Instance","metadata":{"name":"new-1"},"status":{}},
 {"kind":"Backup","metadata":{"name":"done-1"},"status":{"state":"Succeeded"}},
 {"kind":"Backup","metadata":{"name":"bad-1"},"status":{"state":"Failed"}},
 {"kind":"Backup","metadata":{"name":"run-1"},"status":{"state":"Running"}},
 {"kind":"Backup","metadata":{"name":"fresh-1"}},
 {"kind":"Restore","metadata":{"name":"pend-1"},"status":{"state":"Pending"}},
 {"kind":"Restore","metadata":{"name":"del-1"},"status":{"state":"Deleting"}},
 {"kind":"Restore","metadata":{"name":"ok-1"},"status":{"state":"Succeeded"}}]}'
got="$(lifecycle_unfinished_work "${unfinished_list}")"
want='Instance rest-1 (Initializing)
Instance susp-1 (Suspended)
Instance new-1 (no phase yet)
Backup run-1 (Running)
Backup fresh-1 (no state yet)
Restore pend-1 (Pending)
Restore del-1 (Deleting)'
[[ "${got}" == "${want}" ]] || fail "unfinished work: got '${got}', want '${want}'"
got="$(lifecycle_unfinished_work '{"items":[{"kind":"Instance","metadata":{"name":"a"},"status":{"phase":"Ready"}},{"kind":"Backup","metadata":{"name":"b"},"status":{"state":"Failed"}}]}')"
[[ -z "${got}" ]] || fail "unfinished work: a Ready database and a Failed backup read as unfinished: '${got}'"
got="$(lifecycle_unfinished_work '{"items":[]}')"
[[ -z "${got}" ]] || fail "unfinished work: an empty list read as unfinished: '${got}'"
if lifecycle_unfinished_work 'not json' >/dev/null 2>&1; then
  fail "unfinished work: unreadable json read as none"
fi
pass unfinished-work

# --- a full runner disk is the usual reason a kind lifecycle stalls ---
cat >"${FAKE}/docker" <<'EOF'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"${FAKE_LOG}"
case "$1" in
  ps) echo pds-worker ;;
esac
exit 0
EOF
chmod +x "${FAKE}/docker"
hash -r
: >"${FAKE_LOG}"
dump_out="$(lifecycle_dump)"
grep -q '^disk:' <<<"${dump_out}" || fail "dump has no disk section"
grep -q 'docker system df' "${FAKE_LOG}" || fail "dump skipped docker system df: $(cat "${FAKE_LOG}")"
grep -q 'docker exec pds-worker df' "${FAKE_LOG}" || fail "dump skipped the node disk: $(cat "${FAKE_LOG}")"
pass dump-disk

echo "all lifecycle lib tests passed"
