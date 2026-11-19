#!/usr/bin/env bash
#
# Unit tests for scripts/lib/flux-environment.sh. Nothing is started.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=flux-environment.sh
source "${REPO_ROOT}/scripts/lib/flux-environment.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

# The rendered chart source, as the adapter writes it.
cat >"${tmp}/helmrepository.yaml" <<'YAML'
apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: pds-charts
  namespace: flux-system
spec:
  interval: 1h
  type: oci
  url: oci://ghcr.io/percona/charts
YAML

flux_environment_chart_source "${tmp}/helmrepository.yaml" "oci://lab-charts:5000/charts" \
  || fail "the chart source was refused"
grep -qx '  url: oci://lab-charts:5000/charts' "${tmp}/helmrepository.yaml" || fail "url not replaced"
grep -qx '  insecure: true' "${tmp}/helmrepository.yaml" || fail "plain HTTP not allowed"
grep -q 'ghcr.io' "${tmp}/helmrepository.yaml" && fail "the published address is still there"
pass chart-source-points-at-the-lab-registry

flux_environment_chart_source "${tmp}/helmrepository.yaml" "oci://lab-charts:5000/charts" \
  || fail "second run was refused"
[[ "$(grep -c 'insecure: true' "${tmp}/helmrepository.yaml")" == 1 ]] || fail "insecure written twice"
pass chart-source-is-idempotent

printf 'kind: HelmRepository\nspec:\n  interval: 1h\n' >"${tmp}/nourl.yaml"
if flux_environment_chart_source "${tmp}/nourl.yaml" "oci://x/y" 2>/dev/null; then
  fail "a file with no url was accepted"
fi
pass chart-source-refuses-a-file-with-no-url

[[ "$(flux_environment_chart_version "${REPO_ROOT}/charts/pds")" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]] \
  || fail "chart version not read from Chart.yaml"
pass chart-version

echo "flux-environment checks passed"
