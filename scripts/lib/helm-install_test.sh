#!/usr/bin/env bash
#
# Unit tests for scripts/lib/helm-install.sh. Uses fake kubectl and helm on PATH.
# Assignments in this file are inputs to the sourced helpers.
# shellcheck disable=SC2034
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=helm-install.sh
source "${REPO_ROOT}/scripts/lib/helm-install.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

# --- preserve ingress ---
FAKE="$(mktemp -d)"
trap 'rm -rf "${FAKE}"' EXIT
cat >"${FAKE}/kubectl" <<'EOF'
#!/usr/bin/env bash
if [[ -z "${1:-}" ]]; then
  echo "kubectl received an empty first argument" >&2
  exit 1
fi
if [[ "$*" == *"get ingress"* ]]; then
  printf 'ui.example.com nginx'
  exit 0
fi
echo "unexpected kubectl $*" >&2
exit 1
EOF
chmod +x "${FAKE}/kubectl"
PATH="${FAKE}:${PATH}"
KUBECTL_ARGS=()
INGRESS_HOST=""
INGRESS_CLASS="traefik"
EVEREST_NAMESPACE=everest-system
RELEASE=pds
helm_install_preserve_ingress
[[ "${INGRESS_HOST}" == "ui.example.com" ]] || fail "preserve host: ${INGRESS_HOST}"
[[ "${INGRESS_CLASS}" == "nginx" ]] || fail "preserve class: ${INGRESS_CLASS}"
pass preserve-ingress

INGRESS_HOST="keep.me"
helm_install_preserve_ingress
[[ "${INGRESS_HOST}" == "keep.me" ]] || fail "caller host overwritten"
pass preserve-ingress-caller

# --- ingress helm sets ---
HELM_SETS=()
INGRESS_HOST="db.example.com"
INGRESS_CLASS="nginx"
INGRESS_TLS_SECRET="ui-tls"
helm_install_apply_ingress_sets
printf '%s\n' "${HELM_SETS[@]}" | grep -q 'ingress.enabled=true' || fail "ingress enabled"
printf '%s\n' "${HELM_SETS[@]}" | grep -q 'ingress.host=db.example.com' || fail "ingress host"
printf '%s\n' "${HELM_SETS[@]}" | grep -q 'ingress.tls.secretName=ui-tls' || fail "tls secret"
pass ingress-sets

# --- uninstall command shape ---
cat >"${FAKE}/helm" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >"${FAKE_HELM_LOG}"
exit 0
EOF
chmod +x "${FAKE}/helm"
export FAKE_HELM_LOG="${FAKE}/helm.log"
HELM_KUBE_ARGS=()
EVEREST_NAMESPACE=everest-system
RELEASE=pds
helm_install_uninstall
grep -q 'uninstall' "${FAKE_HELM_LOG}" || fail "uninstall missing"
grep -q -- '--no-hooks' "${FAKE_HELM_LOG}" || fail "uninstall should skip hooks"
grep -q -- '--ignore-not-found' "${FAKE_HELM_LOG}" || fail "uninstall should ignore missing"
pass uninstall

# --- upgrade records chart and namespace ---
cat >"${FAKE}/helm" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == "upgrade" && "$2" == "--help" ]]; then
  echo "      --force-conflicts"
  exit 0
fi
printf '%s\n' "$@" >"${FAKE_HELM_LOG}"
exit 0
EOF
chmod +x "${FAKE}/helm"
CHART_REF=/tmp/chart
PLATFORM_TIMEOUT=10s
HELM_SETS=(--set stack.minio.enabled=true)
HELM_VALUE_FILES=(-f /tmp/extra-values.yaml)
helm_install_upgrade
grep -q 'upgrade' "${FAKE_HELM_LOG}" || fail "upgrade missing"
grep -q -- '--install' "${FAKE_HELM_LOG}" || fail "upgrade --install missing"
grep -q 'everest-system' "${FAKE_HELM_LOG}" || fail "namespace missing"
grep -q -- '--force-conflicts' "${FAKE_HELM_LOG}" || fail "force-conflicts missing"
grep -q 'stack.minio.enabled=true' "${FAKE_HELM_LOG}" || fail "helm sets missing"
grep -q -- '-f' "${FAKE_HELM_LOG}" || fail "value file flag missing"
grep -q '/tmp/extra-values.yaml' "${FAKE_HELM_LOG}" || fail "value file path missing"
# --set has to follow -f so an explicit set wins over the values file.
files_at="$(grep -n '/tmp/extra-values.yaml' "${FAKE_HELM_LOG}" | head -1 | cut -d: -f1)"
sets_at="$(grep -n 'stack.minio.enabled=true' "${FAKE_HELM_LOG}" | head -1 | cut -d: -f1)"
[[ "${files_at}" -lt "${sets_at}" ]] || fail "value files should precede --set"
HELM_VALUE_FILES=()
pass upgrade

cat >"${FAKE}/helm" <<'EOF'
#!/usr/bin/env bash
echo "v4.2.4+g3900f43"
EOF
chmod +x "${FAKE}/helm"
[[ "$(helm_install_wait_flag)" == "--wait=legacy" ]] \
  || fail "helm 4 should get the legacy wait strategy, got $(helm_install_wait_flag)"
pass wait-flag-helm4-legacy

cat >"${FAKE}/helm" <<'EOF'
#!/usr/bin/env bash
echo "v3.17.0+gd7c0c5c"
EOF
chmod +x "${FAKE}/helm"
[[ "$(helm_install_wait_flag)" == "--wait" ]] \
  || fail "helm 3 should get the plain wait flag, got $(helm_install_wait_flag)"
pass wait-flag-helm3-plain

cat >"${FAKE}/helm" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "${FAKE}/helm"
[[ "$(helm_install_wait_flag)" == "--wait" ]] \
  || fail "an unreadable helm version should fall back to the plain wait flag"
pass wait-flag-unknown-version-plain

echo "all helm-install tests passed"
