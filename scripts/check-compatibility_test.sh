#!/usr/bin/env bash
#
# Unit tests for scripts/check-compatibility.sh and the helm pin fail.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="${REPO_ROOT}/scripts/check-compatibility.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

[[ -x "${CHECK}" ]] || fail "scripts/check-compatibility.sh must exist and be executable"

# --- fixture helpers ---
write_pin() {
  local dest="$1"
  cat >"${dest}" <<'EOF'
schema: 1

support:
  freshInstall: supported
  upgrade: unsupported
  update:
    from:
      openeverest: ">= 2.0.0-dev.2"

kubernetes:
  min: ">= 1.27.0-0"

pds:
  chart: "0.1.0"
  app: "0.1.0"

openeverest:
  name: openeverest
  repository: https://openeverest.github.io/helm-charts/
  version: "2.0.0-dev.2"

providers:
  pxc:
    name: provider-percona-xtradb-cluster
    alias: provider-pxc
    repository: oci://ghcr.io/openeverest/charts
    version: "0.1.0"
    operator: "1.20.0"
  postgresql:
    name: provider-percona-postgresql
    alias: provider-postgresql
    repository: oci://ghcr.io/openeverest/charts
    version: "0.1.0"
    operator: "3.0.0"

pmm:
  name: pmm
  repository: https://percona.github.io/percona-helm-charts/
  version: "1.9.1"
  app: "3.9.1"

crds:
  instance: core.openeverest.io/v1alpha1
  backup: backup.openeverest.io/v1alpha1

databases:
  mysql:
    engine: Percona XtraDB Cluster
    versionSource: operator-default
  postgresql:
    engine: Percona PostgreSQL
    versionSource: operator-default
EOF
}

write_chart() {
  local dest="$1"
  cat >"${dest}" <<'EOF'
apiVersion: v2
name: pds
version: 0.1.0
appVersion: "0.1.0"
kubeVersion: ">= 1.27.0-0"
dependencies:
  - name: openeverest
    version: 2.0.0-dev.2
    repository: https://openeverest.github.io/helm-charts/
  - name: provider-percona-xtradb-cluster
    version: 0.1.0
    repository: oci://ghcr.io/openeverest/charts
    alias: provider-pxc
  - name: provider-percona-postgresql
    version: 0.1.0
    repository: oci://ghcr.io/openeverest/charts
    alias: provider-postgresql
  - name: pmm
    version: 1.9.1
    repository: https://percona.github.io/percona-helm-charts/
annotations:
  pds.percona.com/platform-charts: |
    openeverest/openeverest 2.0.0-dev.2
    oci://ghcr.io/openeverest/charts/provider-percona-xtradb-cluster 0.1.0
    oci://ghcr.io/openeverest/charts/provider-percona-postgresql 0.1.0
    percona/pmm 1.9.1 (PMM 3.9.1)
EOF
}

write_values() {
  local dest="$1"
  cat >"${dest}" <<'EOF'
platform:
  openeverest:
    repository: https://openeverest.github.io/helm-charts/
    chart: openeverest
    version: 2.0.0-dev.2
  pxcOperator:
    version: 1.20.0
  postgresqlOperator:
    version: 3.0.0
  pmm:
    repository: https://percona.github.io/percona-helm-charts/
    chart: pmm
    version: 1.9.1
    appVersion: 3.9.1
EOF
}

write_docs() {
  local dest="$1"
  cat >"${dest}" <<'EOF'
# Compatibility

Fresh install support: supported.
Upgrade support: unsupported.

Supported matrix: Percona Database Service 0.1.0, OpenEverest 2.0.0-dev.2,
provider-percona-xtradb-cluster 0.1.0 with PXC operator 1.20.0,
provider-percona-postgresql 0.1.0 with PostgreSQL operator 3.0.0,
PMM 1.9.1 (3.9.1). CRDs core.openeverest.io/v1alpha1 and
backup.openeverest.io/v1alpha1. Databases use the operator default.

## Pin-bump evidence

A pin-bump PR must pass .github/workflows/smoke.yml job hub-smoke
before merge. Per-PR kind-platform is not that evidence.

## N to N+1 qualification

Qualify control-plane, provider, operator, CRD, and database
compatibility as one unit.
EOF
}

write_types() {
  local dest="$1"
  mkdir -p "$(dirname "${dest}")"
  cat >"${dest}" <<'EOF'
const APIVersion = "core.openeverest.io/v1alpha1"
const BackupAPIVersion = "backup.openeverest.io/v1alpha1"
EOF
}

setup_fixture() {
  local root="$1"
  mkdir -p "${root}/charts/pds" "${root}/docs" \
    "${root}/adapter/internal/client/everest"
  write_pin "${root}/charts/pds/compatibility.yaml"
  write_chart "${root}/charts/pds/Chart.yaml"
  write_values "${root}/charts/pds/values.yaml"
  write_docs "${root}/docs/compatibility.md"
  write_types "${root}/adapter/internal/client/everest/types.go"
}

run_check() {
  local root="$1"
  shift
  "${CHECK}" --root "${root}" "$@"
}

expect_ok() {
  local name="$1" root="$2"
  local out
  if ! out="$(run_check "${root}" 2>&1)"; then
    fail "${name}: expected pass, got: ${out}"
  fi
  pass "${name}"
}

expect_fail() {
  local name="$1" root="$2" needle="$3"
  local out rc=0
  out="$(run_check "${root}" 2>&1)" || rc=$?
  [[ "${rc}" -ne 0 ]] || fail "${name}: expected fail, got pass: ${out}"
  [[ "${out}" == *"${needle}"* ]] || fail "${name}: stderr missing '${needle}': ${out}"
  [[ "${out}" != *"!"* ]] || fail "${name}: diagnostic contains an exclamation: ${out}"
  pass "${name}"
}

FIXTURE="$(mktemp -d)"
trap 'rm -rf "${FIXTURE}"' EXIT

setup_fixture "${FIXTURE}"
expect_ok matching-fixture "${FIXTURE}"

sed -i.bak 's/version: 2.0.0-dev.2/version: 1.16.2/' \
  "${FIXTURE}/charts/pds/Chart.yaml"
expect_fail chart-openeverest-drift "${FIXTURE}" "2.0.0-dev.2"
expect_fail chart-openeverest-names-got "${FIXTURE}" "1.16.2"
write_chart "${FIXTURE}/charts/pds/Chart.yaml"

sed -i.bak 's/version: 2.0.0-dev.2/version: 9.9.9/' \
  "${FIXTURE}/charts/pds/values.yaml"
expect_fail values-openeverest-drift "${FIXTURE}" "compatibility.yaml"
expect_fail values-openeverest-names-got "${FIXTURE}" "9.9.9"
write_values "${FIXTURE}/charts/pds/values.yaml"

sed -i.bak 's/1.20.0/1.19.0/' \
  "${FIXTURE}/charts/pds/values.yaml"
expect_fail values-operator-drift "${FIXTURE}" "1.20.0"
write_values "${FIXTURE}/charts/pds/values.yaml"

sed -i.bak 's/2.0.0-dev.2/9.9.9/' "${FIXTURE}/docs/compatibility.md"
expect_fail docs-matrix-drift "${FIXTURE}" "2.0.0-dev.2"
write_docs "${FIXTURE}/docs/compatibility.md"

python3 - "${FIXTURE}/docs/compatibility.md" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
text = text.replace(
    "Qualify control-plane, provider, operator, CRD, and database\ncompatibility as one unit.\n",
    "",
)
path.write_text(text)
PY
expect_fail docs-missing-n-to-n1 "${FIXTURE}" "control-plane, provider, operator, CRD, and database"
write_docs "${FIXTURE}/docs/compatibility.md"

python3 - "${FIXTURE}/docs/compatibility.md" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
text = text.replace("unsupported", "not listed")
path.write_text(text)
PY
expect_fail docs-missing-upgrade "${FIXTURE}" "unsupported"
write_docs "${FIXTURE}/docs/compatibility.md"

python3 - "${FIXTURE}/docs/compatibility.md" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
text = text.replace("Fresh install support: supported.", "Install support: omitted.")
path.write_text(text)
PY
expect_fail docs-missing-fresh-install "${FIXTURE}" "fresh install"
write_docs "${FIXTURE}/docs/compatibility.md"

python3 - "${FIXTURE}/docs/compatibility.md" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
text = text.replace("hub-smoke", "kind-smoke")
path.write_text(text)
PY
expect_fail docs-missing-hub-smoke "${FIXTURE}" "hub-smoke"
write_docs "${FIXTURE}/docs/compatibility.md"

sed -i.bak 's/core.openeverest.io\/v1alpha1/core.openeverest.io\/v1/' \
  "${FIXTURE}/adapter/internal/client/everest/types.go"
expect_fail crd-drift "${FIXTURE}" "core.openeverest.io/v1alpha1"
write_types "${FIXTURE}/adapter/internal/client/everest/types.go"

rm -f "${FIXTURE}/charts/pds/compatibility.yaml"
expect_fail missing-pin "${FIXTURE}" "compatibility.yaml"
write_pin "${FIXTURE}/charts/pds/compatibility.yaml"

cat >"${FIXTURE}/charts/pds/compatibility.yaml" <<'EOF'
--- !ruby/hash:Unsafe
openeverest: x
EOF
expect_fail unsafe-yaml-object "${FIXTURE}" "did not parse"
write_pin "${FIXTURE}/charts/pds/compatibility.yaml"

python3 - "${FIXTURE}/charts/pds/compatibility.yaml" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
text = text.replace("  instance: core.openeverest.io/v1alpha1\n", "")
path.write_text(text)
PY
expect_fail pin-missing-required-value "${FIXTURE}" "missing a required value"
write_pin "${FIXTURE}/charts/pds/compatibility.yaml"

python3 - "${FIXTURE}/charts/pds/compatibility.yaml" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
text = text.replace('  chart: "0.1.0"\n', "", 1)
path.write_text(text)
PY
expect_fail pin-missing-eq-value "${FIXTURE}" "missing a required value"
write_pin "${FIXTURE}/charts/pds/compatibility.yaml"

# --- the update floors in support.update.from ---
python3 - "${FIXTURE}/charts/pds/compatibility.yaml" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
path.write_text(path.read_text().replace('      openeverest: ">= 2.0.0-dev.2"\n', '      openeverest: ">= 2.0.0-dev.2"\n      redis: ">= 1.0.0"\n'))
PY
expect_fail update-floor-unknown-component "${FIXTURE}" "redis"
write_pin "${FIXTURE}/charts/pds/compatibility.yaml"

for bad in '"2.0.0-dev.2"' '"> 2.0.0-dev.2"' '"<= 2.0.0-dev.2"' '">= latest"' '""'; do
  python3 - "${FIXTURE}/charts/pds/compatibility.yaml" "${bad}" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
path.write_text(path.read_text().replace('">= 2.0.0-dev.2"', sys.argv[2]))
PY
  expect_fail "update-floor-not-a-minimum-${bad//[^0-9a-z]/_}" "${FIXTURE}" "support.update.from.openeverest"
  write_pin "${FIXTURE}/charts/pds/compatibility.yaml"
done

python3 - "${FIXTURE}/charts/pds/compatibility.yaml" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
path.write_text(path.read_text().replace('    from:\n      openeverest: ">= 2.0.0-dev.2"\n', '    from: ">= 2.0.0-dev.2"\n'))
PY
expect_fail update-from-not-a-map "${FIXTURE}" "support.update.from"
write_pin "${FIXTURE}/charts/pds/compatibility.yaml"

python3 - "${FIXTURE}/charts/pds/compatibility.yaml" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
path.write_text(path.read_text().replace('  update:\n    from:\n      openeverest: ">= 2.0.0-dev.2"\n', ''))
PY
expect_ok update-floors-are-optional "${FIXTURE}"
write_pin "${FIXTURE}/charts/pds/compatibility.yaml"

# --- real repository ---
expect_ok real-repo "${REPO_ROOT}"

# --- helm render fails when values leave the pin ---
command -v helm >/dev/null || fail "helm is required to test the install-time pin fail"
CHART="${REPO_ROOT}/charts/pds"
[[ -f "${CHART}/compatibility.yaml" ]] || fail "chart pin missing"
if ! compgen -G "${CHART}/charts/*.tgz" >/dev/null; then
  echo "fetching chart dependencies for helm pin tests"
  "${REPO_ROOT}/scripts/helm-deps.sh"
fi

helm_out="$(mktemp)"
if helm template pin-check "${CHART}" --set stack.enabled=false \
  --set platform.openeverest.version=0.0.0 >"${helm_out}" 2>&1; then
  fail "helm template accepted platform.openeverest.version=0.0.0: $(cat "${helm_out}")"
fi
grep -q 'compatibility.yaml' "${helm_out}" \
  || fail "helm diagnostic does not name the pin: $(cat "${helm_out}")"
grep -q '0.0.0' "${helm_out}" \
  || fail "helm diagnostic does not name the got version: $(cat "${helm_out}")"
grep -q '2.0.0-dev.4' "${helm_out}" \
  || fail "helm diagnostic does not name the pin version: $(cat "${helm_out}")"
if grep -q '!' "${helm_out}"; then
  fail "helm diagnostic contains an exclamation: $(cat "${helm_out}")"
fi
pass helm-values-mismatch

if helm template pin-check "${CHART}" --set stack.enabled=false \
  --set platform.pmm.repository=https://example.invalid >"${helm_out}" 2>&1; then
  fail "helm template accepted platform.pmm.repository drift: $(cat "${helm_out}")"
fi
grep -q 'platform.pmm.repository' "${helm_out}" \
  || fail "helm diagnostic does not name platform.pmm.repository: $(cat "${helm_out}")"
grep -q 'https://example.invalid' "${helm_out}" \
  || fail "helm diagnostic does not name the got repository: $(cat "${helm_out}")"
if grep -q '!' "${helm_out}"; then
  fail "helm diagnostic contains an exclamation: $(cat "${helm_out}")"
fi
pass helm-pmm-repository-mismatch

# The adapter's database is on by default. It is turned off here, with its
# operator, so this render stays about the version pins. The public URL and
# gitDir are what a hub render requires.
if ! helm template pin-check "${CHART}" \
  --set adapter.hub.publicURL=https://hub.example \
  --set adapter.hub.gitDir=/var/lib/hub-git \
  --set adapter.hub.persistence.enabled=true \
  --set adapter.database.enabled=false \
  --set adapter.database.operator.enabled=false >/dev/null; then
  fail "helm template with matching values failed"
fi
pass helm-values-match

# publish.yml rewrites Chart.yaml version and appVersion to the release
# tag (0.1.0-alpha.1) before helm package. Render must still accept that stamp.
PUBLISH_CHART="$(mktemp -d)/pds"
cp -R "${CHART}" "${PUBLISH_CHART}"
python3 - "${PUBLISH_CHART}/Chart.yaml" <<'PY'
from pathlib import Path
import sys
path = Path(sys.argv[1])
text = path.read_text()
text = text.replace("version: 0.1.0", "version: 0.1.0-alpha.1", 1)
text = text.replace('appVersion: "0.1.0"', 'appVersion: "0.1.0-alpha.1"', 1)
path.write_text(text)
PY
if ! helm template pin-check "${PUBLISH_CHART}" \
  --set adapter.hub.publicURL=https://hub.example \
  --set adapter.hub.gitDir=/var/lib/hub-git \
  --set adapter.hub.persistence.enabled=true \
  --set adapter.database.enabled=false \
  --set adapter.database.operator.enabled=false >/dev/null; then
  fail "helm template rejected a publish-style Chart.yaml version"
fi
pass helm-publish-prerelease-version

MISSING_KEY_CHART="$(mktemp -d)/pds"
cp -R "${CHART}" "${MISSING_KEY_CHART}"
cat >"${MISSING_KEY_CHART}/compatibility.yaml" <<'EOF'
schema: 1
pds:
  chart: "0.1.0"
EOF
if helm template pin-check "${MISSING_KEY_CHART}" --set stack.enabled=false \
  >"${helm_out}" 2>&1; then
  fail "helm template accepted a pin without openeverest: $(cat "${helm_out}")"
fi
grep -q 'missing the openeverest key' "${helm_out}" \
  || fail "helm diagnostic does not name the missing key: $(cat "${helm_out}")"
if grep -q 'did not parse' "${helm_out}"; then
  fail "missing-key pin was reported as a parse error: $(cat "${helm_out}")"
fi
if grep -q '!' "${helm_out}"; then
  fail "helm diagnostic contains an exclamation: $(cat "${helm_out}")"
fi
pass helm-pin-missing-openeverest

MISSING_PROVIDERS_CHART="$(mktemp -d)/pds"
cp -R "${CHART}" "${MISSING_PROVIDERS_CHART}"
cat >"${MISSING_PROVIDERS_CHART}/compatibility.yaml" <<'EOF'
schema: 1
openeverest:
  version: "2.0.0-dev.2"
  repository: https://openeverest.github.io/helm-charts/
EOF
if helm template pin-check "${MISSING_PROVIDERS_CHART}" --set stack.enabled=false \
  >"${helm_out}" 2>&1; then
  fail "helm template accepted a pin without providers: $(cat "${helm_out}")"
fi
grep -q 'missing the providers key' "${helm_out}" \
  || fail "helm diagnostic does not name the missing providers key: $(cat "${helm_out}")"
if grep -q 'error calling index' "${helm_out}"; then
  fail "missing providers produced a template index error: $(cat "${helm_out}")"
fi
if grep -q '!' "${helm_out}"; then
  fail "helm diagnostic contains an exclamation: $(cat "${helm_out}")"
fi
pass helm-pin-missing-providers

BROKEN_PIN_CHART="$(mktemp -d)/pds"
cp -R "${CHART}" "${BROKEN_PIN_CHART}"
printf '%s\n' '{' >"${BROKEN_PIN_CHART}/compatibility.yaml"
if helm template pin-check "${BROKEN_PIN_CHART}" --set stack.enabled=false \
  >"${helm_out}" 2>&1; then
  fail "helm template accepted a malformed pin: $(cat "${helm_out}")"
fi
grep -q 'did not parse' "${helm_out}" \
  || fail "malformed pin was not reported as a parse error: $(cat "${helm_out}")"
if grep -q '!' "${helm_out}"; then
  fail "helm diagnostic contains an exclamation: $(cat "${helm_out}")"
fi
pass helm-pin-malformed

EMPTY_PIN_CHART="$(mktemp -d)/pds"
cp -R "${CHART}" "${EMPTY_PIN_CHART}"
printf '%s\n' '[]' >"${EMPTY_PIN_CHART}/compatibility.yaml"
if helm template pin-check "${EMPTY_PIN_CHART}" --set stack.enabled=false \
  >"${helm_out}" 2>&1; then
  fail "helm template accepted a list pin: $(cat "${helm_out}")"
fi
grep -q 'did not parse' "${helm_out}" \
  || fail "list pin was not reported as a parse error: $(cat "${helm_out}")"
if grep -q 'error calling index' "${helm_out}"; then
  fail "list pin produced a template index error: $(cat "${helm_out}")"
fi
if grep -q '!' "${helm_out}"; then
  fail "helm diagnostic contains an exclamation: $(cat "${helm_out}")"
fi
pass helm-pin-non-map

echo "all compatibility tests passed"
