#!/usr/bin/env bash
#
# Golden `helm template` snapshots for charts/pds.
#
#   scripts/chart-snapshot.sh            check the snapshots against the chart
#   scripts/chart-snapshot.sh --update   regenerate the snapshots
#
# The default snapshot is the one .github/workflows/chart.yml diffs, so its path
# and name must stay charts/pds/tests/template.snap.yaml. CI renders
# with helm 3.17 and helm 4. This script warns when the local helm is neither,
# because a helm upgrade can change rendering and reopen the diff.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART_DIR="${REPO_ROOT}/charts/pds"
SNAP_DIR="${CHART_DIR}/tests"
RELEASE="${RELEASE:-pds}"
HELM="${HELM:-helm}"

UPDATE=false
if [[ "${1:-}" == "--update" ]]; then
  UPDATE=true
elif [[ $# -gt 0 ]]; then
  echo "usage: $(basename "$0") [--update]" >&2
  exit 2
fi

helm_version="$("${HELM}" version --template '{{.Version}}' 2>/dev/null || echo unknown)"
case "${helm_version}" in
  v3.*|v4.*) ;;
  *) echo "warning: CI renders with helm v3.17.0 and v4.2.4, local helm is ${helm_version}" >&2 ;;
esac

# A hub render. helm template has no cluster, so these stand in for the
# origin and the gitDir volume an install sets.
HUB_RENDER=(
  --set adapter.hub.publicURL=https://hub.example
  --set adapter.hub.gitDir=/var/lib/hub-git
  --set adapter.hub.persistence.enabled=true
)

# Connected environment. The values file turns the stack on. The token is not
# in that file.
SPOKE_RENDER=(
  --values "${CHART_DIR}/values-spoke.yaml"
  --set adapter.hub.agentToken=lab-token
)

# name:values-file (empty values file means chart defaults)
CASES=(
  "template:"
  "template.all-features:${SNAP_DIR}/values-all-features.yaml"
  "template.hosted-ui:${SNAP_DIR}/values-hosted-ui.yaml"
)

trim_eof() {
  # Helm 4's full-chart template ends with an extra blank line that helm 3.17
  # does not emit. Trailing blanks are not part of the chart.
  awk '{ line[NR] = $0 } END { n = NR; while (n > 0 && line[n] == "") n--; for (i = 1; i <= n; i++) print line[i] }'
}

strip_helm4() {
  # Helm 4 emits a blank line before each document separator. Strip it so a
  # snapshot generated here matches the helm 3.17 render CI diffs.
  sed '/^$/{N;s/^\n---$/---/;}' | trim_eof
}

parent_templates() {
  # Subchart documents are upstream YAML. The golden keeps this chart's
  # objects, which is where a connected environment's own settings render.
  awk '
    BEGIN { keep = 0 }
    /^---$/ { next }
    /^# Source: / {
      keep = ($0 ~ /^# Source: pds\/templates\//)
      if (keep) {
        print "---"
        print
      }
      next
    }
    keep { print }
  '
}

# grep_all reads everything on stdin before matching. `helm template | grep -q`
# fails under pipefail when grep exits on the first match while helm is still
# writing and helm dies on the closed pipe, so a check that matched reports a
# failure. helm's own exit status still decides the pipe.
grep_all() {
  local input
  input="$(cat)"
  grep -q -- "$1" <<<"${input}"
}

same_snapshot() {
  diff -u <(trim_eof <"$1") <(trim_eof <"$2")
}

# The S3 Job is rendered with stack.enabled=true (helm 4 adds extra blanks
# before ---). Drop those so the golden matches helm 3.17.
s3_strip() {
  awk '
    /^$/ { blanks++; next }
    /^---$/ { print; blanks=0; next }
    {
      for (i = 0; i < blanks; i++) print ""
      blanks = 0
      print
    }
  '
}

status=0
for entry in "${CASES[@]}"; do
  name="${entry%%:*}"
  values="${entry#*:}"
  golden="${SNAP_DIR}/${name}.snap.yaml"
  generated="$(mktemp)"

  args=("${RELEASE}" "${CHART_DIR}" "${HUB_RENDER[@]}")
  # The adapter's database is on by default. These two cases name an external
  # one and turn the operator off so the golden stays this chart's own YAML.
  # values-all-features.yaml is the in-cluster database and its operator.
  if [[ "${name}" == "template" || "${name}" == "template.hosted-ui" ]]; then
    args+=(
      --set adapter.database.existingSecret=hub-db-url
      --set adapter.database.operator.enabled=false
    )
  fi
  if [[ -n "${values}" ]]; then
    args+=(--values "${values}")
  fi

  "${HELM}" template "${args[@]}" | strip_helm4 >"${generated}"

  if [[ "${UPDATE}" == true ]]; then
    mv "${generated}" "${golden}"
    chmod 0644 "${golden}"
    echo "updated ${golden#"${REPO_ROOT}/"}"
    continue
  fi

  if [[ ! -f "${golden}" ]]; then
    echo "missing snapshot ${golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${generated}"
    status=1
    continue
  fi

  if same_snapshot "${golden}" "${generated}"; then
    echo "ok ${golden#"${REPO_ROOT}/"}"
  else
    echo "snapshot drift in ${golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    status=1
  fi
  rm -f "${generated}"
done

# The authorization server's paths reach the adapter. An ingress that sent them
# to the console would answer an agent's discovery request with the SPA's HTML.
ingress_backend() {
  awk -v want="$2" '
    /^ *- path: / { path = $3 }
    path == want && /^ *name: / { print $2; exit }
  ' "$1"
}
for ingress_path in /oauth /.well-known/oauth-authorization-server /.well-known/oauth-protected-resource /mcp; do
  backend="$(ingress_backend "${SNAP_DIR}/template.all-features.snap.yaml" "${ingress_path}")"
  if [[ -z "${backend}" || "${backend}" != *adapter* ]]; then
    echo "the ingress must send ${ingress_path} to the adapter, found '${backend:-nothing}'" >&2
    status=1
  fi
done
for conf_path in 'location /oauth/ {' 'location = /.well-known/oauth-authorization-server {' 'location /.well-known/oauth-protected-resource {' 'location = /mcp {' 'location /mcp/ {'; do
  if ! grep -qF "${conf_path}" "${SNAP_DIR}/template.hosted-ui.snap.yaml"; then
    echo "the UI nginx config must proxy '${conf_path}' to the adapter" >&2
    status=1
  fi
done

if grep -q s3-backup "${SNAP_DIR}/template.snap.yaml" "${SNAP_DIR}/template.all-features.snap.yaml"; then
  echo "S3 backup Job leaked into an adapter-only snapshot (stack.enabled=false)" >&2
  status=1
fi

if grep -q "kind: InstancePreset" "${SNAP_DIR}/template.snap.yaml" "${SNAP_DIR}/template.all-features.snap.yaml"; then
  echo "InstancePreset leaked into an adapter-only snapshot (stack.enabled=false)" >&2
  status=1
fi

# Percona Database Service InstancePresets. Rendered with the stack on so the apply Job is in
# the golden without the OpenEverest subcharts.
presets_golden="${SNAP_DIR}/template.instance-presets.snap.yaml"
presets_generated="$(mktemp)"
"${HELM}" template "${RELEASE}" "${CHART_DIR}" \
  --namespace everest-system \
  "${SPOKE_RENDER[@]}" \
  --show-only templates/instance-presets.yaml | s3_strip >"${presets_generated}"

if [[ "${UPDATE}" == true ]]; then
  mv "${presets_generated}" "${presets_golden}"
  chmod 0644 "${presets_golden}"
  echo "updated ${presets_golden#"${REPO_ROOT}/"}"
else
  if [[ ! -f "${presets_golden}" ]]; then
    echo "missing snapshot ${presets_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${presets_generated}"
    status=1
  elif same_snapshot "${presets_golden}" "${presets_generated}"; then
    echo "ok ${presets_golden#"${REPO_ROOT}/"}"
    rm -f "${presets_generated}"
  else
    echo "snapshot drift in ${presets_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${presets_generated}"
    status=1
  fi
fi

if [[ "${UPDATE}" != true ]] && [[ -f "${presets_golden}" ]]; then
  for name in pds-mysql-dev pds-mysql-staging pds-mysql-prod \
    pds-postgresql-dev pds-postgresql-staging pds-postgresql-prod; do
    if ! grep -q "name: ${name}" "${presets_golden}"; then
      echo "instance presets snapshot is missing ${name}" >&2
      status=1
    fi
  done
  if ! grep -q 'pds.percona.com/preset-version: "1"' "${presets_golden}"; then
    echo "instance presets snapshot is missing preset-version 1" >&2
    status=1
  fi
  if ! grep -q instancepresets.core.openeverest.io "${presets_golden}"; then
    echo "instance presets snapshot does not wait for the InstancePreset CRD" >&2
    status=1
  fi
fi

# S3 BackupStorage Job. Rendered with MinIO off so the snapshot stays the Job
# YAML, not the OpenEverest subcharts. values-spoke.yaml turns the stack on;
# the Job is gated on stack.s3.enabled.
s3_golden="${SNAP_DIR}/template.s3.snap.yaml"
s3_generated="$(mktemp)"
"${HELM}" template "${RELEASE}" "${CHART_DIR}" \
  --namespace everest-system \
  "${SPOKE_RENDER[@]}" \
  --set stack.minio.enabled=false \
  --set stack.s3.enabled=true \
  --set stack.s3.bucket=pds-backups \
  --set stack.s3.existingSecret=s3-credentials \
  --show-only templates/s3-backup-storage-job.yaml | s3_strip >"${s3_generated}"

if [[ "${UPDATE}" == true ]]; then
  mv "${s3_generated}" "${s3_golden}"
  chmod 0644 "${s3_golden}"
  echo "updated ${s3_golden#"${REPO_ROOT}/"}"
else
  if [[ ! -f "${s3_golden}" ]]; then
    echo "missing snapshot ${s3_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${s3_generated}"
    status=1
  elif same_snapshot "${s3_golden}" "${s3_generated}"; then
    echo "ok ${s3_golden#"${REPO_ROOT}/"}"
    rm -f "${s3_generated}"
  else
    echo "snapshot drift in ${s3_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${s3_generated}"
    status=1
  fi
fi

# Database namespace Job. Helm injects managed-by=Helm on objects it owns, so
# the namespace is created by this hook rather than as a Helm Namespace.
dbns_golden="${SNAP_DIR}/template.db-namespace.snap.yaml"
dbns_generated="$(mktemp)"
"${HELM}" template "${RELEASE}" "${CHART_DIR}" \
  --namespace everest-system \
  "${SPOKE_RENDER[@]}" \
  --show-only templates/db-namespace.yaml | s3_strip >"${dbns_generated}"

if [[ "${UPDATE}" == true ]]; then
  mv "${dbns_generated}" "${dbns_golden}"
  chmod 0644 "${dbns_golden}"
  echo "updated ${dbns_golden#"${REPO_ROOT}/"}"
else
  if [[ ! -f "${dbns_golden}" ]]; then
    echo "missing snapshot ${dbns_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${dbns_generated}"
    status=1
  elif same_snapshot "${dbns_golden}" "${dbns_generated}"; then
    echo "ok ${dbns_golden#"${REPO_ROOT}/"}"
    rm -f "${dbns_generated}"
  else
    echo "snapshot drift in ${dbns_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${dbns_generated}"
    status=1
  fi
fi

if [[ "${UPDATE}" != true ]] && [[ -f "${dbns_golden}" ]]; then
  if grep -q '^kind: Namespace$' "${dbns_golden}"; then
    echo "database namespace must not be a Helm Namespace; Helm would stamp managed-by=Helm" >&2
    status=1
  fi
  if ! grep -q 'kubectl create namespace' "${dbns_golden}"; then
    echo "database namespace Job must create the namespace when it is missing" >&2
    status=1
  fi
  if ! grep -q 'app.kubernetes.io/managed-by=everest' "${dbns_golden}"; then
    echo "database namespace Job must label the namespace managed-by=everest" >&2
    status=1
  fi
  if ! grep -q 'helm.sh/hook: pre-install,pre-upgrade' "${dbns_golden}"; then
    echo "database namespace Job must run as a pre-install hook so the namespace exists before other objects" >&2
    status=1
  fi
fi

# Helm-owned Namespace objects cannot keep managed-by=everest. Other Jobs must
# not relabel a namespace Helm already applied.
if grep -R --include='*.yaml' -n '^kind: Namespace$' "${CHART_DIR}/templates"; then
  echo "a chart template owns a Namespace; Helm will stamp managed-by=Helm on it" >&2
  status=1
fi
if grep -R --include='*.yaml' -n 'kubectl label namespace' "${CHART_DIR}/templates" \
  | grep -v 'templates/db-namespace.yaml:'; then
  echo "a chart template other than db-namespace.yaml overwrites a namespace label" >&2
  status=1
fi

token_job="$(mktemp)"
if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${SPOKE_RENDER[@]}" \
    --show-only templates/everest-token-job.yaml | s3_strip >"${token_job}"; then
  if grep -q 'kubectl label namespace' "${token_job}"; then
    echo "everest-token Job overwrites a namespace label; helm 4 upgrade then conflicts" >&2
    status=1
  fi
  if ! grep -q 'POST /v1/auth/token' "${token_job}"; then
    echo "everest-token Job no longer mints an access token" >&2
    status=1
  fi
else
  echo "everest-token Job failed to render" >&2
  status=1
fi
rm -f "${token_job}"

# MinIO. The default snapshot is a hub, so the backup store is not in it;
# this case is what covers the bucket keeping its volume.
minio_golden="${SNAP_DIR}/template.minio.snap.yaml"
minio_generated="$(mktemp)"
"${HELM}" template "${RELEASE}" "${CHART_DIR}" \
  --namespace everest-system \
  "${SPOKE_RENDER[@]}" \
  --show-only templates/minio.yaml | s3_strip >"${minio_generated}"

if [[ "${UPDATE}" == true ]]; then
  mv "${minio_generated}" "${minio_golden}"
  chmod 0644 "${minio_golden}"
  echo "updated ${minio_golden#"${REPO_ROOT}/"}"
else
  if [[ ! -f "${minio_golden}" ]]; then
    echo "missing snapshot ${minio_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${minio_generated}"
    status=1
  elif same_snapshot "${minio_golden}" "${minio_generated}"; then
    echo "ok ${minio_golden#"${REPO_ROOT}/"}"
    rm -f "${minio_generated}"
  else
    echo "snapshot drift in ${minio_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${minio_generated}"
    status=1
  fi
fi

# An emptyDir bucket loses every backup when the pod moves, while the Backup
# records still report success. Nothing in the chart may reintroduce it as the
# default.
if [[ "${UPDATE}" != true ]] && [[ -f "${minio_golden}" ]] && grep -q "emptyDir" "${minio_golden}"; then
  echo "MinIO renders an emptyDir bucket by default; backups would not survive a pod move" >&2
  status=1
fi

# PMM. A connected environment installs it and wires it up in the same install:
# a pre-install hook mints the admin password and a post-install hook mints the
# service account token and creates the MonitoringConfig. The default
# snapshot is a hub, so these cases are what cover it.
pmm_golden() {
  local golden="$1" template="$2"
  shift 2
  local generated
  generated="$(mktemp)"
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system "${SPOKE_RENDER[@]}" "$@" \
    --show-only "${template}" | s3_strip >"${generated}"
  if [[ "${UPDATE}" == true ]]; then
    mv "${generated}" "${golden}"
    chmod 0644 "${golden}"
    echo "updated ${golden#"${REPO_ROOT}/"}"
  elif [[ ! -f "${golden}" ]]; then
    echo "missing snapshot ${golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${generated}"
    status=1
  elif same_snapshot "${golden}" "${generated}"; then
    echo "ok ${golden#"${REPO_ROOT}/"}"
    rm -f "${generated}"
  else
    echo "snapshot drift in ${golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${generated}"
    status=1
  fi
}

pmm_admin_golden="${SNAP_DIR}/template.pmm-admin.snap.yaml"
pmm_config_golden="${SNAP_DIR}/template.monitoring-config.snap.yaml"
pmm_golden "${pmm_admin_golden}" templates/pmm-admin-secret-job.yaml
pmm_golden "${pmm_config_golden}" templates/monitoring-config-job.yaml

if [[ "${UPDATE}" != true ]]; then
  if ! grep -q 'helm.sh/hook: pre-install,pre-upgrade' "${pmm_admin_golden}"; then
    echo "the PMM admin password must exist before pmm-0 starts, so its Job is a pre-install hook" >&2
    status=1
  fi
  if ! grep -q 'value: "true"' <(awk '/name: MINT_TOKEN/ { getline; print }' "${pmm_config_golden}"); then
    echo "the monitoring hook no longer mints the PMM token on a default stack install" >&2
    status=1
  fi
  if ! grep -q 'kind: MonitoringConfig' "${pmm_config_golden}"; then
    echo "the monitoring hook no longer creates the MonitoringConfig" >&2
    status=1
  fi
  if ! grep -q 'rollout restart' "${pmm_config_golden}"; then
    echo "the monitoring hook no longer restarts the control plane after minting a token" >&2
    status=1
  fi

  stack_pmm="$(mktemp)"
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
    "${SPOKE_RENDER[@]}" >"${stack_pmm}" 2>/dev/null
  # Read into a variable: grep -q closing the pipe early would fail awk under
  # pipefail.
  monitoring_env="$(awk '/name: EVEREST_MONITORING_CONFIG/ { getline; print }' "${stack_pmm}")"
  if grep -q '^kind: StatefulSet$' "${stack_pmm}" && [[ "${monitoring_env}" == *'"pmm"'* ]]; then
    echo "ok a connected environment brings PMM and points new databases at it"
  else
    echo "a connected environment no longer brings PMM or names its MonitoringConfig" >&2
    status=1
  fi
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
    "${SPOKE_RENDER[@]}" \
    --set stack.pmm.enabled=false >"${stack_pmm}" 2>/dev/null
  if grep -q '^kind: StatefulSet$' "${stack_pmm}" || grep -q 'name: pds-monitoring-config$' "${stack_pmm}"; then
    echo "stack.pmm.enabled=false still installs PMM or its hook" >&2
    status=1
  else
    echo "ok stack.pmm.enabled=false leaves PMM out"
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      "${HUB_RENDER[@]}" \
      --set adapter.database.existingSecret=hub-db-url \
      --set adapter.database.operator.enabled=false >"${stack_pmm}" 2>/dev/null \
    && ! grep -q '^kind: StatefulSet$' "${stack_pmm}"; then
    echo "ok a hub leaves PMM out without naming stack.pmm"
  else
    echo "a hub fails or still installs PMM" >&2
    status=1
  fi
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
    "${SPOKE_RENDER[@]}" \
    --set stack.pmm.enabled=false \
    --set-string adapter.pmmURL=http://host.docker.internal:18082 \
    --set-string adapter.pmmClientURL=https://192.0.2.1:18443 \
    --set pmm.existingSecret=pds-pmm \
    --show-only templates/monitoring-config-job.yaml >"${stack_pmm}" 2>/dev/null || true
  mint_env="$(awk '/name: MINT_TOKEN/ { getline; print }' "${stack_pmm}")"
  if [[ "${mint_env}" == *'"false"'* ]] && grep -q 'name: pds-pmm' "${stack_pmm}"; then
    echo "ok a PMM outside the release keeps the token it was given"
  else
    echo "the monitoring hook mints a token for a PMM it did not install" >&2
    status=1
  fi
  # PMM installed by Flux from hub Git. Only the subchart is left out: the
  # admin password Job, the token Job and the MonitoringConfig stay in this
  # release, because they name this release's Deployments and Secrets. The Job
  # must not be a hook, or helm waits for a pmm-0 that Flux has not started yet.
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
    "${SPOKE_RENDER[@]}" \
    --set stack.pmm.subchart=false >"${stack_pmm}" 2>/dev/null
  if grep -q '^kind: StatefulSet$' "${stack_pmm}"; then
    echo "stack.pmm.subchart=false still installs the PMM subchart" >&2
    status=1
  else
    echo "ok stack.pmm.subchart=false leaves the PMM subchart out"
  fi
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
    "${SPOKE_RENDER[@]}" \
    --set stack.pmm.subchart=false \
    --show-only templates/monitoring-config-job.yaml >"${stack_pmm}" 2>/dev/null || true
  hook_count="$(grep -c 'helm.sh/hook' "${stack_pmm}" || true)"
  mint_env="$(awk '/name: MINT_TOKEN/ { getline; print }' "${stack_pmm}")"
  if [[ "${hook_count}" == 0 ]] && grep -q '^kind: Job$' "${stack_pmm}" && [[ "${mint_env}" == *'"true"'* ]]; then
    echo "ok a Flux-managed PMM gets a token Job that helm does not wait for"
  else
    echo "the monitoring Job is still a hook, or no longer mints the token, when PMM is Flux-managed" >&2
    status=1
  fi
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
    "${SPOKE_RENDER[@]}" \
    --set stack.pmm.subchart=false \
    --show-only templates/pmm-admin-secret-job.yaml >"${stack_pmm}" 2>/dev/null || true
  if grep -q 'helm.sh/hook: pre-install,pre-upgrade' "${stack_pmm}"; then
    echo "ok a Flux-managed PMM still gets its admin password before it starts"
  else
    echo "a Flux-managed PMM has no admin password Job" >&2
    status=1
  fi
  rm -f "${stack_pmm}"
fi

# The UI shell must be revalidated on every load. Without a Cache-Control
# header a browser keeps the old index.html by its own rules and keeps loading
# the previous build's script after a deploy.
if [[ "${UPDATE}" != true ]]; then
  ui_conf="$(mktemp)"
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    "${HUB_RENDER[@]}" \
    --set adapter.database.existingSecret=hub-db-url \
    --set adapter.database.operator.enabled=false \
    --show-only templates/ui-configmap.yaml >"${ui_conf}" 2>/dev/null || true
  # The console's shell is /app/index.html, since the console lives under /app/.
  if grep -q 'location = /app/index.html' "${ui_conf}" && grep -q 'Cache-Control "no-cache"' "${ui_conf}"; then
    echo "ok the UI shell is revalidated on every load"
  else
    echo "the UI nginx config does not mark index.html no-cache; a deploy would not reach a browser that cached the old shell" >&2
    status=1
  fi
  rm -f "${ui_conf}"
fi

# Mutual exclusion and required S3 fields. stack.enabled stays true so the
# fail templates run; helm still has to load the platform subcharts.
expect_fail() {
  local name="$1"
  shift
  # Hub values come first so a later --set in the case still wins, and the
  # case fails for the reason it names rather than for a missing backend.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
      "${HUB_RENDER[@]}" "$@" >/dev/null 2>&1; then
    echo "expected ${name} to fail helm template" >&2
    status=1
  else
    echo "ok fail ${name}"
  fi
}

# Like expect_fail, and the render has to say why: a value that was removed
# names where its replacement lives, so the operator is not left with a bare
# template error.
expect_fail_saying() {
  local name="$1"
  local message="$2"
  shift 2
  local out
  if out="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
      "${HUB_RENDER[@]}" "$@" 2>&1 >/dev/null)"; then
    echo "expected ${name} to fail helm template" >&2
    status=1
  elif [[ "${out}" != *"${message}"* ]]; then
    echo "expected ${name} to fail saying '${message}', got:" >&2
    printf '%s\n' "${out}" >&2
    status=1
  else
    echo "ok fail ${name}"
  fi
}

if [[ "${UPDATE}" != true ]]; then
  expect_fail "minio+s3" \
    "${SPOKE_RENDER[@]}" \
    --set stack.minio.enabled=true \
    --set stack.s3.enabled=true \
    --set stack.s3.bucket=pds-backups \
    --set stack.s3.existingSecret=s3-credentials
  expect_fail "s3 missing bucket" \
    "${SPOKE_RENDER[@]}" \
    --set stack.minio.enabled=false \
    --set stack.s3.enabled=true \
    --set stack.s3.existingSecret=s3-credentials
  expect_fail "s3 missing secret" \
    "${SPOKE_RENDER[@]}" \
    --set stack.minio.enabled=false \
    --set stack.s3.enabled=true \
    --set stack.s3.bucket=pds-backups
  expect_fail "pmm without stack" \
    --set stack.enabled=false \
    --set stack.pmm.enabled=true
  expect_fail "servicenow plaintext url" \
    --set servicenow.enabled=true \
    --set-string servicenow.apiKey=snapshot-api-key \
    --set-string servicenow.url=http://example.service-now.com
  expect_fail "servicenow missing api key" \
    --set servicenow.enabled=true
  # The operator's Anthropic and Cursor keys are gone. Each value that used to
  # carry one fails the render and says where the key is pasted instead.
  # An upgrade with --reuse-values carries the old chart's defaults, which were
  # all empty or false. Those say nothing was set, so they must still render.
  reuse_render="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.existingSecret=hub-db-url \
      --set adapter.database.operator.enabled=false \
      --set assistant.enabled=false --set-string assistant.apiKey= --set-string assistant.baseUrl= \
      --set-string assistant.existingSecret= --set-string assistant.secretName= \
      --set cursor.enabled=false --set-string cursor.apiKey= \
      --set-string cursor.existingSecret= --set-string cursor.secretName= \
      >"${reuse_render}" 2>&1; then
    echo "the old chart's empty assistant and cursor defaults failed to render:" >&2
    cat "${reuse_render}" >&2
    status=1
  else
    echo "ok empty assistant and cursor defaults from an old release still render"
  fi
  rm -f "${reuse_render}"
  for removed in assistant.enabled=true assistant.apiKey=removed assistant.baseUrl=https://proxy.example \
      assistant.existingSecret=mine assistant.secretName=mine \
      cursor.enabled=true cursor.apiKey=removed cursor.existingSecret=mine cursor.secretName=mine; do
    expect_fail_saying "removed value ${removed%%=*}" \
      "${removed%%=*} was removed. Paste the key in the console" \
      --set "${removed}"
  done
  # The MCP device flow is gone. Each value that configured it fails the render
  # and says so. An upgrade with --reuse-values carries the old defaults, which
  # were empty, and those must still render.
  oidc_render="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.existingSecret=hub-db-url \
      --set adapter.database.operator.enabled=false \
      --set-string adapter.oidc.audience= --set-string adapter.oidc.deviceClientId= \
      >"${oidc_render}" 2>&1; then
    echo "the old chart's empty device flow defaults failed to render:" >&2
    cat "${oidc_render}" >&2
    status=1
  else
    echo "ok empty device flow defaults from an old release still render"
  fi
  rm -f "${oidc_render}"
  for removed in adapter.oidc.audience=https://pds.example/api adapter.oidc.deviceClientId=pds-device; do
    expect_fail_saying "removed value ${removed%%=*}" \
      "${removed%%=*} was removed. The MCP device flow is gone" \
      --set "${removed}"
  done
  sn_render="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.existingSecret=hub-db-url \
      --set adapter.database.operator.enabled=false \
      --set servicenow.enabled=true \
      --set-string servicenow.apiKey=snapshot-api-key \
      --set-string servicenow.url=https://example.service-now.com \
      >"${sn_render}"; then
    echo "expected servicenow.apiKey without a client token to render" >&2
    status=1
  elif ! grep -q 'SN_API_KEY: "snapshot-api-key"' "${sn_render}"; then
    echo "expected servicenow.apiKey without a client token to render SN_API_KEY" >&2
    status=1
  elif grep -q 'SN_CLIENT_TOKEN:' "${sn_render}"; then
    echo "expected servicenow without a client token not to render SN_CLIENT_TOKEN" >&2
    status=1
  elif ! grep -q 'name: SN_URL' "${sn_render}" || ! grep -q 'https://example.service-now.com' "${sn_render}"; then
    echo "expected servicenow.url to render SN_URL" >&2
    status=1
  else
    echo "ok servicenow api key without client token"
  fi
  rm -f "${sn_render}"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.enabled=false \
      --set adapter.database.operator.enabled=false \
      --set adapter.installInstancePresets=true \
      --show-only templates/instance-presets.yaml | grep_all pds-mysql-dev; then
    echo "expected adapter.installInstancePresets to render Percona Database Service InstancePresets (pds-mysql-dev)" >&2
    status=1
  else
    echo "ok adapter.installInstancePresets"
  fi
  # The spoke shape has to render before anything is asserted about it. These
  # are negative checks, and a render that fails produces no output, so without
  # this they report ok on a chart that cannot be installed at all.
  # --set stack.enabled=false beats values-spoke.yaml, so this render is the
  # stack forced off. The snapshot further down is the overlay winning over a
  # chart default of stack off.
  spoke_render="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --values "${CHART_DIR}/values-spoke.yaml" \
      --set adapter.hub.agentToken=lab-token >"${spoke_render}" 2>&1; then
    echo "values-spoke.yaml with the stack off failed to render: $(cat "${spoke_render}")" >&2
    status=1
  else
    echo "ok values-spoke.yaml renders with the stack off"
  fi
  # An agent never serves the customer API, so it keeps no sessions and needs
  # no database. One rendered here would be a PostgreSQL cluster every spoke
  # provisions and never reads.
  if grep -qE 'kind: PerconaPGCluster|name: DATABASE_URL' "${spoke_render}"; then
    echo "spoke profile rendered a database the agent never reads" >&2
    status=1
  else
    echo "ok values-spoke.yaml omits the adapter database"
  fi
  if grep -q 'kind: Deployment' <(
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --values "${CHART_DIR}/values-spoke.yaml" \
      --set adapter.hub.agentToken=lab-token \
      --show-only templates/ui-deployment.yaml 2>/dev/null
  ); then
    echo "spoke profile rendered the UI Deployment" >&2
    status=1
  else
    echo "ok values-spoke.yaml omits UI"
  fi
  rm -f "${spoke_render}"
  # An Ingress that routes / at a Service the release did not create returns
  # 503 with nothing on the chart side saying why.
  # An Ingress on a spoke is refused outright rather than rendered without the
  # UI backend: an agent serves nothing to route to. Asserting the refusal is
  # what the old check meant to say, and it said nothing, because the render it
  # grepped never succeeded.
  spoke_ingress="$(mktemp)"
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --values "${CHART_DIR}/values-spoke.yaml" \
      --set adapter.hub.agentToken=lab-token \
      --set ingress.enabled=true \
      --set ingress.host=spoke.example.com >"${spoke_ingress}" 2>&1; then
    echo "spoke profile accepted an Ingress with nothing to route to" >&2
    status=1
  elif grep -q 'ingress.enabled is not supported when adapter.role is agent' "${spoke_ingress}"; then
    echo "ok values-spoke.yaml refuses an Ingress"
  else
    echo "spoke profile refused an Ingress for the wrong reason: $(cat "${spoke_ingress}")" >&2
    status=1
  fi
  rm -f "${spoke_ingress}"
  hub_adapter="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.existingSecret=hub-db-url \
      --set adapter.database.operator.enabled=false \
      --show-only templates/adapter-deployment.yaml >"${hub_adapter}"; then
    echo "hub profile failed to render the adapter Deployment" >&2
    status=1
  else
    if ! grep -q 'HUB_PUBLIC_URL' "${hub_adapter}"; then
      echo "hub profile did not render HUB_PUBLIC_URL" >&2
      status=1
    else
      echo "ok chart defaults render the hub settings"
    fi
    # Exact suffix match so a shorter flag name does not catch a longer one.
    feature_env_value() {
      awk -v name="ADAPTER_FEATURE_${1}" '
        $0 ~ "- name: " name "$" { getline; gsub(/["]/, "", $2); print $2; exit }
      ' "${hub_adapter}"
    }
    feature_env_present() {
      grep -Eq "^[[:space:]]+- name: ADAPTER_FEATURE_${1}$" "${hub_adapter}"
    }
    # These names are retired. The hub must not set the variable. A deployment
    # that still sets one starts with one warning, and the name is not published.
    retired_ok=true
    for surface in LOGS ALERT_ANALYSIS INVESTIGATE RECOMMENDATIONS PLATFORM_STATUS ALERTS \
        ASSISTANT_OPENAI ASSISTANT_GEMINI ASSISTANT_OPENROUTER; do
      if feature_env_present "${surface}"; then
        echo "hub profile still sets ADAPTER_FEATURE_${surface}" >&2
        retired_ok=false
        status=1
      fi
    done
    if [[ "${retired_ok}" == true ]]; then
      echo "ok chart defaults do not set retired flags"
    fi
    # Activity reads the adapter database, so a hub serves it. The other
    # shared surfaces stay off because they still need one cluster.
    if [[ "$(feature_env_value ACTIVITY)" == "true" ]]; then
      echo "ok chart defaults turn activity on"
    else
      echo "hub profile left ACTIVITY off (got $(feature_env_value ACTIVITY))" >&2
      status=1
    fi
    hub_on_ok=true
    for surface in DATABASES CLUSTERS BACKUPS CONNECTIONS SUPPORT CHAT ASSISTANT METRICS; do
      if feature_env_present "${surface}"; then
        echo "hub profile overrode ${surface}, which stays on for a hub" >&2
        hub_on_ok=false
        status=1
      fi
    done
    if [[ "${hub_on_ok}" == true ]]; then
      echo "ok chart defaults leave core surfaces on"
    fi
  fi
  rm -f "${hub_adapter}"
  # The OpenAI, Gemini and OpenRouter providers are gone. Values that still
  # name them must render nothing: no Secret, no environment variable, no grant.
  removed_render="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --values "${SNAP_DIR}/values-all-features.yaml" \
      --set openai.enabled=true --set openai.apiKey=removed \
      --set gemini.enabled=true --set gemini.apiKey=removed \
      --set openrouter.enabled=true --set openrouter.apiKey=removed \
      >"${removed_render}"; then
    echo "values that still name a removed provider failed to render" >&2
    status=1
  elif grep -Eqi 'openai|gemini|openrouter' "${removed_render}"; then
    echo "a removed provider still renders:" >&2
    grep -Eni 'openai|gemini|openrouter' "${removed_render}" >&2
    status=1
  else
    echo "ok no OpenAI, Gemini or OpenRouter resource renders"
  fi
  rm -f "${removed_render}"
  # An agent with nowhere to beat exits at startup while helm reports success.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.database.enabled=false \
      --set adapter.database.operator.enabled=false \
      --set adapter.role=agent >/dev/null 2>&1; then
    echo "expected adapter.role=agent without a hub URL to fail" >&2
    status=1
  else
    echo "ok fail agent role without a hub URL"
  fi
  # An agent with a URL but no token exits at startup while helm reports success.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.database.enabled=false \
      --set adapter.database.operator.enabled=false \
      --set adapter.role=agent \
      --set adapter.hub.url=http://hub.example \
      --set adapter.hub.environmentName=spoke-a >/dev/null 2>&1; then
    echo "expected adapter.role=agent without an agent token to fail" >&2
    status=1
  else
    echo "ok fail agent role without an agent token"
  fi
  # The token is a credential. Putting it in the Deployment env makes it
  # readable to anyone who can get the pod spec.
  agent_token="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.database.enabled=false \
      --set adapter.database.operator.enabled=false \
      --set adapter.role=agent \
      --set adapter.hub.url=http://hub.example \
      --set adapter.hub.environmentName=spoke-a \
      --set adapter.hub.agentToken=lab-secret \
      --show-only templates/adapter-deployment.yaml >"${agent_token}"; then
    echo "agent with a token failed to render the adapter Deployment" >&2
    status=1
  else
    if ! grep -q secretKeyRef "${agent_token}"; then
      echo "agent token was not mounted through secretKeyRef" >&2
      status=1
    elif grep -q 'value: "lab-secret"' "${agent_token}"; then
      echo "agent token was inlined in the Deployment env" >&2
      status=1
    else
      echo "ok agent token is mounted through secretKeyRef"
    fi
  fi
  rm -f "${agent_token}"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.database.enabled=false \
      --set adapter.database.operator.enabled=false \
      --set adapter.role=agent \
      --set adapter.hub.url=http://hub.example \
      --set adapter.hub.environmentName=spoke-a \
      --set adapter.hub.agentToken=lab-secret \
      --show-only templates/hub-agent-token-secret.yaml | grep_all 'lab-secret'; then
    echo "expected the chart to write the agent token into a Secret" >&2
    status=1
  else
    echo "ok agent token Secret holds the value"
  fi
  # An environment was called a cluster, so a release already installed carries
  # adapter.hub.clusterName and agentClusters in its values. Both still render
  # the settings the adapter reads and name themselves in the notes.
  agent_render() {
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.database.enabled=false \
      --set adapter.database.operator.enabled=false \
      --set adapter.role=agent \
      --set adapter.hub.url=http://hub.example \
      --set adapter.hub.agentToken=lab-secret \
      "$@"
  }
  env_value() {
    awk -v key="$1" '$0 ~ "- name: " key "$" { getline; gsub(/"/, "", $2); print $2; exit }'
  }
  agent_env="$(agent_render --set adapter.hub.environmentName=env-a \
    --set 'adapter.hub.agentEnvironments={env-a,env-b}' \
    --show-only templates/adapter-deployment.yaml)"
  if [[ "$(env_value ENVIRONMENT_NAME <<<"${agent_env}")" == "env-a" \
      && "$(env_value HUB_AGENT_ENVIRONMENTS <<<"${agent_env}")" == "env-a,env-b" \
      && "${agent_env}" != *"CLUSTER_NAME"* && "${agent_env}" != *"HUB_AGENT_CLUSTERS"* ]]; then
    echo "ok agent environment settings render under the environment names"
  else
    echo "expected ENVIRONMENT_NAME and HUB_AGENT_ENVIRONMENTS and no CLUSTER_NAME or HUB_AGENT_CLUSTERS" >&2
    status=1
  fi
  agent_notes="$(agent_render --set adapter.hub.environmentName=env-a \
    --set 'adapter.hub.agentEnvironments={env-a}' --set snapshotNotes=true \
    --show-only templates/snapshot-notes.yaml)"
  if grep -qi deprecated <<<"${agent_notes}"; then
    echo "the notes call current value keys deprecated" >&2
    status=1
  else
    echo "ok current value keys are not called deprecated"
  fi
  legacy_args=(--set adapter.hub.clusterName=env-old --set 'adapter.hub.agentClusters={env-old}')
  legacy_env="$(agent_render "${legacy_args[@]}" --show-only templates/adapter-deployment.yaml)"
  if [[ "$(env_value ENVIRONMENT_NAME <<<"${legacy_env}")" == "env-old" \
      && "$(env_value HUB_AGENT_ENVIRONMENTS <<<"${legacy_env}")" == "env-old" ]]; then
    echo "ok the old clusterName and agentClusters keys still render"
  else
    echo "expected adapter.hub.clusterName and agentClusters to render ENVIRONMENT_NAME and HUB_AGENT_ENVIRONMENTS" >&2
    status=1
  fi
  legacy_notes="$(agent_render "${legacy_args[@]}" --set snapshotNotes=true \
    --show-only templates/snapshot-notes.yaml)"
  if grep -q 'adapter.hub.clusterName is deprecated: use adapter.hub.environmentName' <<<"${legacy_notes}" \
      && grep -q 'adapter.hub.agentClusters is deprecated: use adapter.hub.agentEnvironments' <<<"${legacy_notes}"; then
    echo "ok the old value keys are named as deprecated in the notes"
  else
    echo "expected the notes to name adapter.hub.clusterName and agentClusters as deprecated" >&2
    status=1
  fi
  both_env="$(agent_render "${legacy_args[@]}" --set adapter.hub.environmentName=env-new \
    --set 'adapter.hub.agentEnvironments={env-new}' --show-only templates/adapter-deployment.yaml)"
  if [[ "$(env_value ENVIRONMENT_NAME <<<"${both_env}")" == "env-new" \
      && "$(env_value HUB_AGENT_ENVIRONMENTS <<<"${both_env}")" == "env-new" ]]; then
    echo "ok the environment value keys win over the old ones"
  else
    echo "expected adapter.hub.environmentName to win over adapter.hub.clusterName" >&2
    status=1
  fi
  if agent_missing="$(agent_render 2>&1)"; then
    echo "expected adapter.role=agent without an environment name to fail" >&2
    status=1
  elif grep -q 'adapter.hub.environmentName is required' <<<"${agent_missing}"; then
    echo "ok fail agent role without an environment name"
  else
    echo "expected the failure to name adapter.hub.environmentName: ${agent_missing}" >&2
    status=1
  fi
  # gitDir on an emptyDir is one restart from pruning every spoke.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.gitDir=/var/lib/hub-git >/dev/null 2>&1; then
    echo "expected adapter.hub.gitDir without storage to fail" >&2
    status=1
  else
    echo "ok fail hub git dir without storage"
  fi
  # GitHub and a local gitDir are two backends. Writing GitHub while loading
  # inventory from the directory lists clusters that are not in GitHub.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.gitDir=/var/lib/hub-git \
      --set adapter.hub.persistence.enabled=true \
      --set adapter.hub.github.org=percona-cloud \
      --set adapter.hub.github.existingSecret=hub-github >/dev/null 2>&1; then
    echo "expected adapter.hub.gitDir with github.org to fail" >&2
    status=1
  else
    echo "ok fail hub git dir with github org"
  fi
  # A hub that authenticates as a GitHub App gets the three App settings from
  # its Secret and no static token. Setting both is a fatal adapter start, so
  # the chart never renders both.
  app_render="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    --set stack.enabled=false \
    --set adapter.hub.publicURL=https://hub.example \
    --set adapter.hub.github.org=percona-cloud \
    --set adapter.hub.github.existingSecret=hub-github \
    --set adapter.hub.github.registryTokenKey=HUB_REGISTRY_TOKEN \
    --set adapter.hub.github.registryTokenDistinct=true \
    --set adapter.hub.github.appIdKey=HUB_GITHUB_APP_ID \
    --set adapter.hub.github.appInstallationIdKey=HUB_GITHUB_APP_INSTALLATION_ID \
    --set adapter.hub.github.appPrivateKeyKey=HUB_GITHUB_APP_PRIVATE_KEY 2>&1)" || true
  if grep -q 'name: HUB_GITHUB_APP_ID$' <<<"${app_render}" \
      && grep -q 'name: HUB_GITHUB_APP_INSTALLATION_ID$' <<<"${app_render}" \
      && grep -q 'name: HUB_GITHUB_APP_PRIVATE_KEY$' <<<"${app_render}" \
      && grep -q 'name: HUB_REGISTRY_TOKEN$' <<<"${app_render}" \
      && ! grep -q 'name: HUB_GITHUB_TOKEN$' <<<"${app_render}"; then
    echo "ok hub github app env replaces the token"
  else
    echo "expected the App env, the registry token and no HUB_GITHUB_TOKEN with adapter.hub.github.app*Key" >&2
    status=1
  fi
  # The adapter hides the registry token from enrollment only while it equals
  # the GitHub token. With an App there is no GitHub token to compare, so a
  # registry token reused from the old static token would reach every
  # environment. Helm cannot compare the Secret values, so the operator has to
  # attest that the registry token is a separate credential.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.publicURL=https://hub.example \
      --set adapter.hub.github.org=percona-cloud \
      --set adapter.hub.github.existingSecret=hub-github \
      --set adapter.hub.github.registryTokenKey=HUB_REGISTRY_TOKEN \
      --set adapter.hub.github.appIdKey=HUB_GITHUB_APP_ID \
      --set adapter.hub.github.appInstallationIdKey=HUB_GITHUB_APP_INSTALLATION_ID \
      --set adapter.hub.github.appPrivateKeyKey=HUB_GITHUB_APP_PRIVATE_KEY >/dev/null 2>&1; then
    echo "expected an App with a registry token and no registryTokenDistinct to fail" >&2
    status=1
  else
    echo "ok fail hub github app registry token without attestation"
  fi
  # A quoted false is a non-empty string, so a truthiness test would let it
  # through as if it were the attestation.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.publicURL=https://hub.example \
      --set adapter.hub.github.org=percona-cloud \
      --set adapter.hub.github.existingSecret=hub-github \
      --set adapter.hub.github.registryTokenKey=HUB_REGISTRY_TOKEN \
      --set-string adapter.hub.github.registryTokenDistinct=false \
      --set adapter.hub.github.appIdKey=HUB_GITHUB_APP_ID \
      --set adapter.hub.github.appInstallationIdKey=HUB_GITHUB_APP_INSTALLATION_ID \
      --set adapter.hub.github.appPrivateKeyKey=HUB_GITHUB_APP_PRIVATE_KEY >/dev/null 2>&1; then
    echo "expected a quoted false registryTokenDistinct to fail" >&2
    status=1
  else
    echo "ok fail hub github app quoted false attestation"
  fi
  # A partial App is refused here rather than at adapter start.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.publicURL=https://hub.example \
      --set adapter.hub.github.org=percona-cloud \
      --set adapter.hub.github.existingSecret=hub-github \
      --set adapter.hub.github.appIdKey=HUB_GITHUB_APP_ID >/dev/null 2>&1; then
    echo "expected a partial GitHub App key set to fail" >&2
    status=1
  else
    echo "ok fail partial hub github app"
  fi
  # An extra volume that is not mounted at gitDir is not storage for it.
  scratch_values="$(mktemp)"
  cat > "${scratch_values}" <<'EOF'
adapter:
  hub:
    gitDir: /var/lib/hub-git
  extraVolumes:
    - name: scratch
      emptyDir: {}
EOF
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --values "${scratch_values}" >/dev/null 2>&1; then
    echo "expected adapter.hub.gitDir with an unrelated extraVolume to fail" >&2
    status=1
  else
    echo "ok fail hub git dir with unrelated extraVolumes"
  fi
  cat > "${scratch_values}" <<'EOF'
adapter:
  hub:
    gitDir: /var/lib/hub-git
    operatorToken: test-operator
    publicURL: https://hub.example
  extraVolumeMounts:
    - name: hub-git
      mountPath: /var/lib/hub-git
  extraVolumes:
    - name: hub-git
      emptyDir: {}
EOF
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.database.enabled=false \
      --set adapter.database.operator.enabled=false \
      --values "${scratch_values}" >/dev/null; then
    echo "ok hub git dir mounted via extraVolumeMounts"
  else
    echo "expected adapter.hub.gitDir with a matching extraVolumeMount to render" >&2
    status=1
  fi
  rm -f "${scratch_values}"
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.gitDir=/var/lib/hub-git \
      --set adapter.hub.publicURL=https://hub.example \
      --set adapter.hub.persistence.enabled=true \
      --show-only templates/adapter-deployment.yaml 2>/dev/null | grep_all 'HUB_OPERATOR_TOKEN'; then
    echo "expected no HUB_OPERATOR_TOKEN env on a hub that was given no token" >&2
    status=1
  else
    echo "ok hub without an operator token renders, and the adapter mints one"
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.gitDir=/var/lib/hub-git \
      --set adapter.hub.operatorToken=test-operator \
      --set adapter.hub.persistence.enabled=true >/dev/null 2>&1; then
    echo "expected a hub without publicURL to fail" >&2
    status=1
  else
    echo "ok fail hub without public URL"
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --values "${CHART_DIR}/values-hub-lab.yaml" \
      --show-only templates/adapter-deployment.yaml | grep_all 'HUB_OPERATOR_TOKEN'; then
    echo "ok values-hub-lab.yaml renders HUB_OPERATOR_TOKEN"
  else
    echo "expected values-hub-lab.yaml to render HUB_OPERATOR_TOKEN" >&2
    status=1
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --values "${CHART_DIR}/values-hub-lab.yaml" \
      --show-only templates/adapter-deployment.yaml | grep_all 'HUB_OAUTH_ISSUER'; then
    echo "ok values-hub-lab.yaml renders HUB_OAUTH_ISSUER"
  else
    echo "expected values-hub-lab.yaml to render HUB_OAUTH_ISSUER" >&2
    status=1
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set stack.enabled=false \
      --set adapter.hub.gitDir=/var/lib/hub-git \
      --set adapter.hub.publicURL=https://hub.example \
      --set adapter.hub.persistence.enabled=true \
      --show-only templates/adapter-deployment.yaml 2>/dev/null | grep_all 'HUB_OAUTH_ISSUER'; then
    echo "expected no HUB_OAUTH_ISSUER env on a hub that did not set one, so the public URL decides" >&2
    status=1
  else
    echo "ok a hub without oauthIssuer renders no HUB_OAUTH_ISSUER"
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --values "${CHART_DIR}/values-hub-lab.yaml" \
      --show-only templates/adapter-deployment.yaml | grep_all 'type: Recreate'; then
    echo "ok hub adapter Deployment uses Recreate"
  else
    echo "expected a hub adapter Deployment to use Recreate" >&2
    status=1
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --values "${CHART_DIR}/values-hub-lab.yaml" \
      --set adapter.hub.persistence.enabled=true >/dev/null 2>&1; then
    echo "expected values-hub-lab.yaml plus persistence to fail" >&2
    status=1
  else
    echo "ok fail hub git dir with persistence and a lab extraVolumeMount"
  fi

  # The adapter's own database. The connection URL carries a password, so the
  # thing worth asserting is that no rendered object ever holds one: both
  # shapes have to reach it through a secretKeyRef.
  expect_fail "hub database with both operators" \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set stack.enabled=true
  expect_fail "hub database with no source" \
    --set stack.enabled=false \
    --set adapter.database.enabled=true \
    --set adapter.database.inCluster.enabled=false
  expect_fail "hub database image major version" \
    --set stack.enabled=false \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set adapter.database.inCluster.postgresVersion=17
  expect_fail "hub database URL overridden through extraEnv" \
    --set stack.enabled=false \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set 'adapter.extraEnv[0].name=DATABASE_URL' \
    --set 'adapter.extraEnv[0].value=postgres://u:p@h/db'
  expect_fail "hub database operator without a database" \
    --set stack.enabled=false \
    --set adapter.database.enabled=false \
    --set adapter.database.operator.enabled=true
  # A tag alongside a digest still carries a major, so it is compared rather
  # than skipped with digest only references.
  expect_fail "hub database image tag beside a digest" \
    --set stack.enabled=false \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set adapter.database.inCluster.images.postgres=docker.io/percona/pg:17.2@sha256:abc
  # An untagged image resolves through latest, which can move to another
  # PostgreSQL major under a running cluster. A digest carries no major to
  # read and is immutable, so it is allowed through.
  expect_fail "hub database image without a tag" \
    --set stack.enabled=false \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set adapter.database.inCluster.images.postgres=docker.io/percona/percona-distribution-postgresql
  # Turning the operator off on a hub leaves the in-cluster database with
  # nothing to reconcile it.
  expect_fail "hub database with nothing to reconcile it" \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=false

  # The development organization. It is the one place the chart knows a password, so
  # what matters is that it stays off by default, refuses a password too short
  # for the adapter to store, and runs the same image the adapter does.
  bootstrap_args=(
    "${HUB_RENDER[@]}"
    --set adapter.database.existingSecret=hub-db-url
    --set adapter.database.operator.enabled=false
    --set adapter.auth.bootstrapOrganization.enabled=true
  )
  expect_fail "bootstrap organization with no password" "${bootstrap_args[@]}"
  expect_fail "bootstrap organization with a short password" "${bootstrap_args[@]}" \
    --set adapter.auth.bootstrapOrganization.password=short
  expect_fail "bootstrap organization with a malformed served organization" "${bootstrap_args[@]}" \
    --set adapter.auth.bootstrapOrganization.password=devpassword123 \
    --set adapter.state.organization=Dev_Organization
  expect_fail "bootstrap organization when the adapter serves every organization" "${bootstrap_args[@]}" \
    --set adapter.auth.bootstrapOrganization.password=devpassword123 \
    --set-string 'adapter.state.organization=*'
  bootstrap_star_err="$(
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${bootstrap_args[@]}" \
      --set adapter.auth.bootstrapOrganization.password=devpassword123 \
      --set-string 'adapter.state.organization=*' \
      2>&1 || true
  )"
  if grep -q 'seeding a development organization only makes sense for one named organization' <<<"${bootstrap_star_err}" \
      && ! grep -q 'start with a letter' <<<"${bootstrap_star_err}"; then
    echo "ok bootstrap on a star names the every-organization refuse"
  else
    echo "expected an explicit refuse of bootstrap when the adapter serves every organization, got: ${bootstrap_star_err}" >&2
    status=1
  fi
  expect_fail "bootstrap organization without a database" \
    --set stack.enabled=false \
    --set adapter.database.enabled=false \
    --set adapter.auth.bootstrapOrganization.enabled=true \
    --set adapter.auth.bootstrapOrganization.password=devpassword123

  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.existingSecret=hub-db-url \
      --set adapter.database.operator.enabled=false \
      --show-only templates/bootstrap-organization-job.yaml >/dev/null 2>&1; then
    echo "the development organization rendered with bootstrapOrganization off" >&2
    status=1
  else
    echo "ok no development organization by default"
  fi

  bootstrap_job="$(mktemp)"
  "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${bootstrap_args[@]}" \
    --set adapter.auth.bootstrapOrganization.password=devpassword123 \
    --show-only templates/bootstrap-organization-job.yaml >"${bootstrap_job}" 2>/dev/null
  if grep -q '\- organization' "${bootstrap_job}" \
      && grep -q '\- bootstrap' "${bootstrap_job}" \
      && grep -q 'key: BOOTSTRAP_PASSWORD' "${bootstrap_job}"; then
    echo "ok development organization runs adapter organization bootstrap"
  else
    echo "expected the development organization Job to run adapter organization bootstrap" >&2
    status=1
  fi
  # The password reaches the Job through a Secret rather than the pod spec, so
  # it is not readable to anyone who can get the Job.
  if grep -q 'value: "devpassword123"' "${bootstrap_job}"; then
    echo "the development organization password was rendered into the pod spec" >&2
    status=1
  else
    echo "ok development organization password comes through a secretKeyRef"
  fi
  rm -f "${bootstrap_job}"

  seeded="$(
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${bootstrap_args[@]}" \
      --set adapter.auth.bootstrapOrganization.password=devpassword123 \
      --set-string adapter.state.organization=acme \
      --show-only templates/bootstrap-organization-job.yaml 2>/dev/null \
      | awk '/- bootstrap$/ { getline; gsub(/[ "-]/, "", $0); print; exit }'
  )"
  served="$(
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${bootstrap_args[@]}" \
      --set adapter.auth.bootstrapOrganization.password=devpassword123 \
      --set-string adapter.state.organization=acme \
      --show-only templates/adapter-deployment.yaml 2>/dev/null \
      | awk '/- name: HUB_ORGANIZATION$/ { getline; gsub(/[ "]/, "", $0); sub(/^value:/, "", $0); print; exit }'
  )"
  if [[ -n "${seeded}" ]] && [[ "${seeded}" == "${served}" ]]; then
    echo "ok development organization is the organization the adapter serves (${seeded})"
  else
    echo "the seeded organization ${seeded:-<none>} is not the served organization ${served:-<none>}: it would sign in and then be refused" >&2
    status=1
  fi

  # A star pin serves every organization that has a row. The chart has to render
  # HUB_ORGANIZATION as that star and refuse to seed a development organization:
  # there is no one id to put a password on.
  star_args=(
    "${HUB_RENDER[@]}"
    --set adapter.database.existingSecret=hub-db-url
    --set adapter.database.operator.enabled=false
    --set-string 'adapter.state.organization=*'
  )
  star_deploy="$(
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${star_args[@]}" \
      --show-only templates/adapter-deployment.yaml 2>/dev/null
  )"
  star_organization="$(
    awk '/- name: HUB_ORGANIZATION$/ { getline; gsub(/^[[:space:]]+value: /, "", $0); gsub(/"/, "", $0); print; exit }' <<<"${star_deploy}"
  )"
  if [[ "${star_organization}" == '*' ]]; then
    echo "ok star organization reaches the adapter"
  else
    echo "expected HUB_ORGANIZATION \"*\", got organization ${star_organization:-<none>}" >&2
    status=1
  fi
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${star_args[@]}" \
      --show-only templates/bootstrap-organization-job.yaml >/dev/null 2>&1; then
    echo "the development organization rendered when the adapter serves every organization" >&2
    status=1
  else
    echo "ok no development organization when the adapter serves every organization"
  fi
  # Helm 3 `helm install --dry-run` still talks to a cluster (it probes
  # localhost:8080). Helm 4 does not. Render NOTES through helm template
  # so both CI jobs can check them without kubeconfig.
  star_notes="$(
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${star_args[@]}" \
      --set snapshotNotes=true \
      --show-only templates/snapshot-notes.yaml 2>/dev/null
  )"
  if grep -q 'adapter organization create <id>' <<<"${star_notes}" \
      && grep -q 'A session for an organization that has a row stays signed in' <<<"${star_notes}" \
      && ! grep -q 'adapter organization create \*' <<<"${star_notes}"; then
    echo "ok star notes onboard any valid id"
  else
    echo "expected NOTES to onboard any valid id and not print adapter organization create *" >&2
    status=1
  fi

  # A session is bounded twice. An idle window longer than the lifetime would
  # never apply, and the adapter refuses to start on it, so the chart has to be
  # able to render both.
  session_env="$(
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.existingSecret=hub-db-url \
      --set adapter.database.operator.enabled=false \
      --set adapter.auth.sessionLifetime=48h \
      --set adapter.auth.sessionIdleTimeout=2h \
      --set adapter.auth.inviteTTL=12h \
      --show-only templates/adapter-deployment.yaml 2>/dev/null
  )"
  if grep -q 'value: "48h"' <<<"${session_env}" \
      && grep -q 'value: "2h"' <<<"${session_env}" \
      && grep -q 'value: "12h"' <<<"${session_env}"; then
    echo "ok session bounds reach the adapter from values"
  else
    echo "expected SESSION_LIFETIME, SESSION_IDLE_TIMEOUT and INVITE_TTL from values" >&2
    status=1
  fi

  db_env() {
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.enabled=true \
      --set adapter.database.operator.enabled=true \
      "$@" \
      --show-only templates/adapter-deployment.yaml 2>/dev/null \
      | awk '/- name: DATABASE_URL$/ { found = 1 } found { print; if (++n >= 5) exit }'
  }

  in_cluster="$(db_env)"
  if grep -q 'secretKeyRef' <<<"${in_cluster}" \
      && grep -q "name: ${RELEASE}-hub-db-pguser-hub" <<<"${in_cluster}" \
      && grep -q 'key: uri' <<<"${in_cluster}"; then
    echo "ok hub database URL comes from the operator minted Secret"
  else
    echo "expected DATABASE_URL from the pguser Secret, got: ${in_cluster}" >&2
    status=1
  fi

  external="$(db_env --set adapter.database.existingSecret=hub-db-url)"
  if grep -q 'secretKeyRef' <<<"${external}" \
      && grep -q 'name: hub-db-url' <<<"${external}" \
      && grep -q 'key: DATABASE_URL' <<<"${external}"; then
    echo "ok hub database URL comes from a supplied Secret"
  else
    echo "expected DATABASE_URL from the supplied Secret, got: ${external}" >&2
    status=1
  fi

  # A supplied Secret means the database already exists, so nothing is rendered
  # for it in the cluster.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.enabled=true \
      --set adapter.database.existingSecret=hub-db-url 2>/dev/null \
      | grep_all 'kind: PerconaPGCluster'; then
    echo "expected no PerconaPGCluster when existingSecret names an external database" >&2
    status=1
  else
    echo "ok external hub database renders no cluster object"
  fi

  # The cluster object is applied by a hook that has to run before the release,
  # because the adapter Deployment waits on a Secret that only exists once the
  # operator has reconciled it. A post-install hook would deadlock --wait.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.enabled=true \
      --set adapter.database.operator.enabled=true \
      --show-only templates/hub-database.yaml 2>/dev/null \
      | grep_all 'helm.sh/hook: pre-install,pre-upgrade'; then
    echo "ok hub database cluster is applied before the release"
  else
    echo "expected the hub database hook to be pre-install, or --wait deadlocks" >&2
    status=1
  fi

  # The fullname is already truncated to 63, so the hook objects that append to
  # it can overflow the name limit and be refused by the API server. Scoped to
  # the names this feature adds: other objects in this chart overflow the same
  # way at this release length, which is a separate problem.
  #
  # Any indentation, not just top level. The cluster name lives inside the hook
  # Job's heredoc, which is where it was missed the first time.
  long_name="$(printf 'x%.0s' $(seq 1 58))"
  overflow="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${HUB_RENDER[@]}" \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set "fullnameOverride=${long_name}" 2>/dev/null \
    | awk '/name: /{ n = $2; gsub(/"/, "", n); if (length(n) > 63 && (n ~ /hub-database/ || n ~ /hub-db/)) print n }' | sort -u)"
  if [[ -n "${overflow}" ]]; then
    echo "a hub database object name is over the 63 character limit: ${overflow}" >&2
    status=1
  else
    echo "ok hub database object names stay within the name limit"
  fi

  # The whole point of the Secret indirection.
  leak="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${HUB_RENDER[@]}" 2>/dev/null \
    | grep -Ein 'postgres(ql)?://[^[:space:]"]*:[^[:space:]"]*@' || true)"
  if [[ -n "${leak}" ]]; then
    # The match is a credential. Printing it verbatim would put the password
    # into the CI log of the very check that exists to keep it out, so only the
    # count and the line numbers are reported. Render locally to see the value.
    echo "a rendered object carries a PostgreSQL URL with credentials" >&2
    echo "matches: $(wc -l <<<"${leak}" | tr -d " ") at rendered line(s) $(cut -d: -f1 <<<"${leak}" | paste -sd, -)" >&2
    status=1
  else
    echo "ok no rendered object carries a connection URL"
  fi

  # The key that opens every stored credential must not reach the rendered
  # release, and extraEnv must not be able to shadow the Secret reference.
  expect_fail "state encryption key overridden through extraEnv" \
    --set stack.enabled=false \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set 'adapter.extraEnv[0].name=STATE_ENCRYPTION_KEY' \
    --set 'adapter.extraEnv[0].value=aGVsbG8='

  state_env() {
    "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.enabled=true \
      --set adapter.database.operator.enabled=true \
      "$@" \
      --show-only templates/adapter-deployment.yaml 2>/dev/null \
      | awk '/- name: STATE_ENCRYPTION_KEY$/ { found = 1 } found { print; if (++n >= 5) exit }'
  }

  minted="$(state_env)"
  if grep -q 'secretKeyRef' <<<"${minted}" \
      && grep -q "name: ${RELEASE}-state-key" <<<"${minted}" \
      && grep -q 'key: STATE_ENCRYPTION_KEY' <<<"${minted}"; then
    echo "ok state encryption key comes from the minted Secret"
  else
    echo "expected STATE_ENCRYPTION_KEY from the minted Secret, got: ${minted}" >&2
    status=1
  fi

  supplied="$(state_env --set adapter.state.existingSecret=hub-state-key)"
  if grep -q 'name: hub-state-key' <<<"${supplied}"; then
    echo "ok state encryption key comes from a supplied Secret"
  else
    echo "expected STATE_ENCRYPTION_KEY from the supplied Secret, got: ${supplied}" >&2
    status=1
  fi

  # A supplied Secret means the key is managed elsewhere, so nothing mints one
  # over it.
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      "${HUB_RENDER[@]}" \
      --set adapter.database.enabled=true \
      --set adapter.database.operator.enabled=true \
      --set adapter.state.existingSecret=hub-state-key 2>/dev/null \
      | grep_all 'app.kubernetes.io/component: state-key'; then
    echo "expected no key hook when existingSecret names a key you supply" >&2
    status=1
  else
    echo "ok supplied state encryption key renders no hook"
  fi

  # The key has to exist before the adapter Deployment is applied, or its
  # secretKeyRef leaves the pod in CreateContainerConfigError. Weight 5 puts
  # the hook ahead of the database hook at 10 as well.
  key_hook="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${HUB_RENDER[@]}" \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --show-only templates/state-key-job.yaml 2>/dev/null)"
  if grep -q 'helm.sh/hook: pre-install,pre-upgrade' <<<"${key_hook}" \
      && grep -q 'helm.sh/hook-weight: "5"' <<<"${key_hook}"; then
    echo "ok state encryption key is minted before the release"
  else
    echo "expected the state key hook to be pre-install at weight 5" >&2
    status=1
  fi

  # The hook must never be able to replace a key that has already sealed
  # credentials, so it creates and never updates, applies or deletes.
  if grep -qE '"(update|patch|delete)"' <<<"${key_hook}"; then
    echo "the state key hook can replace a key that has already sealed credentials" >&2
    status=1
  else
    echo "ok state key hook cannot replace an existing key"
  fi

  # Rendering twice has to agree. A key generated at render time would differ
  # on every run and every upgrade would orphan the credentials already stored.
  second="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${HUB_RENDER[@]}" \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --show-only templates/state-key-job.yaml 2>/dev/null)"
  if [[ "${key_hook}" == "${second}" ]]; then
    echo "ok state encryption key rendering is deterministic"
  else
    echo "the state key template does not render the same twice" >&2
    status=1
  fi

  # Neither key may reach the adapter as a literal. Both are read through a
  # secretKeyRef, so a `value:` under either env name is the leak, whatever the
  # string happens to be. A retired key is a live key: it opens every row that
  # still names it, so it belongs in the Secret beside the primary one.
  #
  # This is checked structurally rather than by grepping for a key shaped
  # string. The first version of this check looked for `STATE_ENCRYPTION_KEY: `,
  # which never matches the `- name:` / `value:` pair Kubernetes env uses, so it
  # passed while retired keys were being rendered in full.
  key_env="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${HUB_RENDER[@]}" \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true 2>/dev/null \
    | awk '
        /- name: STATE_ENCRYPTION_KEY(S_RETIRED)?$/ { show = 6 }
        show { print; show-- }
      ')"
  if grep -qE '^ *value:' <<<"${key_env}"; then
    echo "a state encryption key is rendered as a literal value rather than a Secret reference" >&2
    status=1
  else
    echo "ok neither state encryption key is rendered as a value"
  fi

  # Both keys have to reach the adapter through the Secret, never as a value.
  retired="$("${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    "${HUB_RENDER[@]}" \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true 2>/dev/null \
    | awk '/- name: STATE_ENCRYPTION_KEYS_RETIRED$/ { found = 1 } found { print; if (++n >= 6) exit }')"
  if grep -q 'secretKeyRef' <<<"${retired}" && grep -q 'optional: true' <<<"${retired}"; then
    echo "ok retired keys come from the Secret and are optional"
  else
    echo "expected STATE_ENCRYPTION_KEYS_RETIRED from an optional secretKeyRef, got: ${retired}" >&2
    status=1
  fi

  expect_fail "retired keys overridden through extraEnv" \
    --set stack.enabled=false \
    --set adapter.database.enabled=true \
    --set adapter.database.operator.enabled=true \
    --set 'adapter.extraEnv[0].name=STATE_ENCRYPTION_KEYS_RETIRED' \
    --set 'adapter.extraEnv[0].value=aGVsbG8='

  # The mint Job must only ever create. Any write verb on an existing Secret is
  # a way to replace a key that has already sealed credentials, and the script
  # must not need one either.
  if grep -q 'kubectl label' <<<"${key_hook}" || grep -q 'kubectl patch' <<<"${key_hook}"; then
    echo "the state key hook runs a write its Role does not grant" >&2
    status=1
  else
    echo "ok state key hook runs only the verbs its Role grants"
  fi

  # The chart default is a hub. publicURL and one Git backend are required,
  # and the message has to name whichever of them is missing. A render that
  # has both is a hub: no OpenEverest, MinIO, monitoring server, or worker.
  bare_hub="$(mktemp)"
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system >"${bare_hub}" 2>&1; then
    echo "expected a render without publicURL and a Git backend to fail" >&2
    status=1
  elif grep -q 'adapter.hub.publicURL' "${bare_hub}" \
      && grep -q 'adapter.hub.gitDir' "${bare_hub}" \
      && grep -q 'adapter.hub.github.org' "${bare_hub}"; then
    echo "ok fail hub without publicURL and a git backend"
  else
    echo "a hub render without publicURL and a Git backend failed without naming them: $(cat "${bare_hub}")" >&2
    status=1
  fi
  rm -f "${bare_hub}"
  missing_url="$(mktemp)"
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.hub.gitDir=/var/lib/hub-git \
      --set adapter.hub.persistence.enabled=true >"${missing_url}" 2>&1; then
    echo "expected a hub without publicURL to fail" >&2
    status=1
  elif grep -q 'adapter.hub.publicURL' "${missing_url}"; then
    echo "ok fail hub without publicURL names the missing value"
  else
    echo "a hub without publicURL failed without naming it: $(cat "${missing_url}")" >&2
    status=1
  fi
  rm -f "${missing_url}"
  missing_backend="$(mktemp)"
  if "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.hub.publicURL=https://hub.example >"${missing_backend}" 2>&1; then
    echo "expected a hub without a Git backend to fail" >&2
    status=1
  elif grep -q 'adapter.hub.gitDir' "${missing_backend}" \
      && grep -q 'adapter.hub.github.org' "${missing_backend}"; then
    echo "ok fail hub without a git backend names the missing value"
  else
    echo "a hub without a Git backend failed without naming it: $(cat "${missing_backend}")" >&2
    status=1
  fi
  rm -f "${missing_backend}"
  hub_render="$(mktemp)"
  if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
      --namespace everest-system \
      --set adapter.hub.publicURL=https://hub.example \
      --set adapter.hub.gitDir=/var/lib/hub-git \
      --set adapter.hub.persistence.enabled=true >"${hub_render}" 2>&1; then
    echo "a hub render with publicURL and a Git backend failed: $(cat "${hub_render}")" >&2
    status=1
  elif grep -qE 'charts/openeverest/|templates/minio.yaml|charts/pmm-server/|kind: Worker' "${hub_render}"; then
    echo "a hub render included OpenEverest, MinIO, monitoring, or a worker" >&2
    status=1
  elif ! grep -q 'HUB_PUBLIC_URL' "${hub_render}" || ! grep -q 'HUB_GIT_DIR' "${hub_render}"; then
    echo "a hub render did not set the public URL and gitDir" >&2
    status=1
  else
    echo "ok default render is a hub once publicURL and a Git backend are set"
  fi
  rm -f "${hub_render}"
fi

# Connected-environment overlay. A values file applied first stands in for a chart
# default of stack.enabled false. --set would beat the overlay, so this is not
# a --set. values-spoke.yaml is applied after it and keeps OpenEverest, the
# providers, MinIO and monitoring.
spoke_default="$(mktemp)"
cat >"${spoke_default}" <<'EOF'
stack:
  enabled: false
EOF
spoke_full="$(mktemp)"
spoke_generated="$(mktemp)"
spoke_golden="${SNAP_DIR}/template.spoke.snap.yaml"
if ! "${HELM}" template "${RELEASE}" "${CHART_DIR}" \
    --namespace everest-system \
    --values "${spoke_default}" \
    --values "${CHART_DIR}/values-spoke.yaml" \
    --set adapter.hub.agentTokenExistingSecret=hub-agent-token \
    | strip_helm4 >"${spoke_full}"; then
  echo "values-spoke.yaml failed to render over a chart default with the stack off: $(cat "${spoke_full}")" >&2
  status=1
else
  spoke_ok=1
  spoke_renders() {
    local label="$1" pattern="$2"
    if ! grep -qF -- "${pattern}" "${spoke_full}"; then
      echo "connected environment render is missing ${label}" >&2
      spoke_ok=0
      status=1
    fi
  }
  spoke_renders "OpenEverest" "# Source: pds/charts/openeverest/templates/everest-server/deployment.yaml"
  spoke_renders "the PXC provider" "# Source: pds/charts/provider-pxc/templates/deployment.yaml"
  spoke_renders "the PostgreSQL provider" "# Source: pds/charts/provider-postgresql/templates/deployment.yaml"
  spoke_renders "MinIO" "# Source: pds/templates/minio.yaml"
  spoke_renders "monitoring" "# Source: pds/charts/pmm-server/templates/statefulset.yaml"
  if grep -qF '# Source: pds/templates/ui-deployment.yaml' "${spoke_full}"; then
    echo "connected environment render included the UI" >&2
    spoke_ok=0
    status=1
  fi
  if grep -qE 'victoria-logs|pds-vlogs|PMM_LOGS_URL' "${spoke_full}"; then
    echo "connected environment render included VictoriaLogs" >&2
    spoke_ok=0
    status=1
  fi
  if grep -qE 'kind: PerconaPGCluster|name: DATABASE_URL' "${spoke_full}"; then
    echo "connected environment render included the adapter database" >&2
    spoke_ok=0
    status=1
  fi
  if [[ "${spoke_ok}" == 1 ]]; then
    echo "ok values-spoke.yaml keeps the stack when the chart default turns it off"
  fi
  parent_templates <"${spoke_full}" >"${spoke_generated}"
  if [[ "${spoke_ok}" != 1 ]]; then
    rm -f "${spoke_generated}"
  elif [[ "${UPDATE}" == true ]]; then
    mv "${spoke_generated}" "${spoke_golden}"
    chmod 0644 "${spoke_golden}"
    echo "updated ${spoke_golden#"${REPO_ROOT}/"}"
  elif [[ ! -f "${spoke_golden}" ]]; then
    echo "missing snapshot ${spoke_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${spoke_generated}"
    status=1
  elif same_snapshot "${spoke_golden}" "${spoke_generated}"; then
    echo "ok ${spoke_golden#"${REPO_ROOT}/"}"
    rm -f "${spoke_generated}"
  else
    echo "snapshot drift in ${spoke_golden#"${REPO_ROOT}/"}; run scripts/chart-snapshot.sh --update" >&2
    rm -f "${spoke_generated}"
    status=1
  fi
fi
rm -f "${spoke_default}" "${spoke_full}" "${spoke_generated}"

exit "${status}"
