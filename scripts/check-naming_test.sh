#!/usr/bin/env bash
#
# Unit tests for scripts/check-naming.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="${REPO_ROOT}/scripts/check-naming.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

[[ -x "${CHECK}" ]] || fail "scripts/check-naming.sh must exist and be executable"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# run_on writes one file into a throwaway tree and checks only that tree, so a
# case cannot be answered by something else in the repository.
run_on() {
  local path="$1" body="$2" tree
  tree="$(mktemp -d "${WORK}/tree.XXXXXX")"
  mkdir -p "${tree}/$(dirname "${path}")"
  printf '%s\n' "${body}" >"${tree}/${path}"
  (cd "${tree}" && git init -q . && git add -A && "${CHECK}" "${tree}") >"${WORK}/out" 2>&1
}

expect_ok() {
  local label="$1" path="$2" body="$3"
  if run_on "${path}" "${body}"; then
    pass "${label}"
  else
    cat "${WORK}/out" >&2
    fail "${label}: expected a pass"
  fi
}

expect_fail() {
  local label="$1" path="$2" body="$3" needle="$4"
  if run_on "${path}" "${body}"; then
    fail "${label}: expected a failure"
  fi
  grep -q "${needle}" "${WORK}/out" || {
    cat "${WORK}/out" >&2
    fail "${label}: the report does not name ${needle}"
  }
  pass "${label}"
}

expect_ok "clean source passes" \
  "adapter/internal/api/server.go" \
  'package api // PDS serves this from pds.percona.com/profile'

expect_fail "the old npm scope and module path fail" \
  "adapter/internal/api/server.go" \
  'import "github.com/percona/percona-services/adapter/internal/engine"' \
  "percona-services"

expect_fail "the old annotation domain fails" \
  "charts/pds/templates/instance-presets.yaml" \
  'services.percona.com/profile: dev' \
  "services.percona.com"

expect_fail "the old release and resource prefix fails" \
  "scripts/up.sh" \
  'RELEASE=basecamp-agent' \
  "basecamp"

expect_fail "the dead product names fail" \
  "apps/ui/src/lib/product.ts" \
  'export const productName = "Percona Cloud";' \
  "Percona Cloud"

expect_fail "the older dead product name fails" \
  "docs/SPEC.md" \
  'Percona Basecamp provisions databases.' \
  "Percona Basecamp"

expect_fail "the spaced product name fails" \
  "adapter/internal/api/connect_bootstrap.sh" \
  'echo "no adapter image found. Install Percona Services first." >&2' \
  "Percona Services"

# The identifiers the rename deliberately left alone. A lint that trips on
# these would push someone into breaking a working reference to satisfy it.
expect_ok "the ServiceNow connector keeps its name" \
  "docs/architecture.md" \
  'The Percona ServiceNow integration opens a case.'

expect_ok "the former GitHub repository path is allowed" \
  "CONTRIBUTING.md" \
  'git clone git@github.com:percona/basecamp.git'

expect_ok "the published image names are allowed" \
  ".github/workflows/publish.yml" \
  'adapter=ghcr.io/percona/percona-services-adapter'

expect_fail "the old published chart path fails" \
  "README.md" \
  '  oci://ghcr.io/percona/charts/percona-services \' \
  "percona-services"

expect_ok "the external GitHub org is allowed" \
  "adapter/internal/config/config.go" \
  'const defaultHubGitHubOrg = "percona-cloud"'

expect_ok "a line that opts out is allowed" \
  "adapter/internal/service/support.go" \
  'const legacyTicketMarker = "Reported by the Percona Basecamp control plane." // allow-legacy-name'

expect_ok "the naming rules may spell the old names out" \
  "AGENTS.md" \
  'Two earlier names, Percona Basecamp and Percona Cloud, are dead.'

expect_fail "a deleted runbook path is no longer allowed" \
  "docs/migration-to-pds.md" \
  'The release was called percona-services.' \
  "percona-services"

expect_fail "the shouted env var form fails" \
  "adapter/internal/api/connect.go" \
  'Set PERCONA_SERVICES_CHART to a charts/pds checkout.' \
  "PERCONA_SERVICES"

expect_fail "a second legacy spelling on an allowed line still fails" \
  ".github/workflows/publish.yml" \
  'adapter=ghcr.io/percona/percona-services-adapter # see charts/percona-services' \
  "percona-services"

expect_fail "a legacy path fails even when the file is clean" \
  "charts/percona-services/Chart.yaml" \
  'name: pds' \
  "the path itself carries a legacy name"

expect_fail "the old public route fails" \
  "apps/mcp/internal/cli/cli.go" \
  'resp, err := hc.Get(origin + "/api/clusters")' \
  "/api/clusters"

expect_fail "the old agent route fails" \
  "scripts/lib/lab.sh" \
  'curl "${HUB}/api/internal/clusters/${NAME}/heartbeat"' \
  "/api/internal/clusters"

expect_fail "the old query parameter fails" \
  "apps/ui/src/lib/api.ts" \
  'fetch(`/api/databases?cluster=${name}`)' \
  "?cluster="

expect_fail "the old chart value fails" \
  "scripts/up.sh" \
  'helm upgrade --set adapter.hub.clusterName=spoke-a' \
  "adapter.hub.clusterName"

expect_fail "a Go identifier that names an environment a cluster fails" \
  "adapter/internal/hub/join.go" \
  'func ValidClusterName(name string) bool { return true }' \
  "ValidClusterName"

expect_fail "a TypeScript identifier that names an environment a cluster fails" \
  "apps/ui/src/features/environments/api.ts" \
  'export const useClusterList = () => [];' \
  "useClusterList"

expect_ok "a Kubernetes term in an identifier passes" \
  "adapter/internal/client/kube/kube.go" \
  'func NewInCluster() (*Client, error) { return nil, rest.InClusterConfig() }'

expect_ok "a Kubernetes cluster in prose passes" \
  "adapter/internal/engine/exposure.go" \
  '// Address is the host a client outside the cluster connects to.'

expect_ok "an old name on a line marked as reading old state passes" \
  "adapter/internal/config/config.go" \
  'name := lookup("HUB_AGENT_CLUSTERS", "") // allow-legacy-name'

expect_ok "a file that answers the old names passes" \
  "adapter/internal/api/legacy_names.go" \
  'const legacyAPIPrefix = "/api/clusters"'

expect_ok "prose may quote the old route" \
  "docs/architecture.md" \
  'The old /api/clusters route answers for one release.'

expect_fail "the word tenant fails in code" \
  "adapter/internal/service/identity.go" \
  'func (i *Identity) CreateTenant(ctx context.Context, tenant string) error { return nil }' \
  "tenant"

expect_fail "the word tenant fails in a variable name that carries it" \
  "apps/ui/src/lib/session.ts" \
  'export const e2eTenant = "dev";' \
  "Tenant"

expect_fail "the word tenant fails in an environment variable" \
  "deploy/compose/pds.env.example" \
  'HUB_TENANT=acme' \
  "TENANT"

expect_fail "the word tenant fails in prose" \
  "docs/deployment.md" \
  'Every route needs a signed in tenant.' \
  "tenant"

expect_fail "an allow-legacy-name marker does not excuse the word tenant" \
  "adapter/internal/config/config.go" \
  'name := lookup("HUB_TENANT", "") // allow-legacy-name' \
  "tenant"

expect_fail "a path that carries the word tenant fails" \
  "adapter/internal/hub/tenantgit.go" \
  'package hub' \
  "the path itself carries the word tenant"

expect_ok "the upgrade file may name the old columns" \
  "adapter/internal/store/legacy_tenant.go" \
  'const column = "tenant_id"'

expect_ok "the upgrade tests may name them" \
  "adapter/internal/store/legacy_tenant_test.go" \
  'const column = "tenant_id"'

expect_ok "the schema the upgrade tests start from may name the old columns" \
  "adapter/internal/store/testdata/release_before_organizations.sql" \
  'CREATE TABLE hub_tokens (tenant_id text);'

expect_ok "the glossary may define the term it replaced" \
  "CONTEXT.md" \
  '_Avoid_: Tenant'

expect_fail "notes under docs are not exempt" \
  "docs/notes/auth0.md" \
  'Auth0 calls this multi-tenant.' \
  "tenant"

expect_fail "a vendored Flux manifest is not exempt" \
  "clusters/example/flux-system/gotk-components.yaml" \
  'This enables multi-tenant authentication.' \
  "tenant"

# A tree the lint cannot enumerate is not a clean tree.
if "${CHECK}" "${WORK}" >"${WORK}/out" 2>&1; then
  fail "a directory that is not a checkout should not report clean"
fi
grep -q "Is this a git checkout" "${WORK}/out" \
  || { cat "${WORK}/out" >&2; fail "the refusal does not say why"; }
pass "a directory that is not a checkout is refused, not passed"

echo "all naming checks passed"
