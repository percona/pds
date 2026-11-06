#!/usr/bin/env bash
#
# Unit tests for scripts/lib/hub-install.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=hub-install.sh
source "${REPO_ROOT}/scripts/lib/hub-install.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

reset() {
  HELM_SETS=(--set adapter.image.tag=dev)
  HELM_VALUE_FILES=()
  CHART_DIR="${REPO_ROOT}/charts/pds"
  HUB_PUBLIC_URL=""
  HUB_GITHUB_ORG=""
  HUB_GITHUB_SECRET=""
  HUB_GIT_URL=""
  HUB_CLUSTER_REGION=""
  INGRESS_HOST=""
  INGRESS_TLS_SECRET=""
}

joined() { printf '%s\n' "${HELM_SETS[@]}"; }

reset
if hub_install_prepare 2>/dev/null; then
  fail "missing public URL should fail"
fi
pass missing-public-url

reset
INGRESS_HOST="hub.example.com"
hub_install_prepare || fail "ingress host should derive a public URL"
[[ "${HUB_PUBLIC_URL}" == "http://hub.example.com" ]] || fail "derived url: ${HUB_PUBLIC_URL}"
joined | grep -q 'adapter.hub.publicURL=http://hub.example.com' || fail "public url set"
joined | grep -q 'adapter.hub.gitDir=/var/lib/hub-git' || fail "git dir"
joined | grep -q 'adapter.hub.persistence.enabled=true' || fail "persistence"
joined | grep -q 'stack.enabled=false' || fail "stack off"
if printf '%s\n' ${HELM_VALUE_FILES[@]+"${HELM_VALUE_FILES[@]}"} | grep -q 'values-hub.yaml'; then
  fail "values-hub.yaml is folded into the chart defaults"
fi
joined | grep -q 'adapter.hub.github.org=' && fail "git dir path set a GitHub org"
pass git-dir-from-ingress

reset
HUB_PUBLIC_URL="https://hub.example.com"
INGRESS_TLS_SECRET="ui-tls"
INGRESS_HOST="other.example.com"
hub_install_prepare || fail "explicit public URL should win"
[[ "${HUB_PUBLIC_URL}" == "https://hub.example.com" ]] || fail "explicit url overwritten"
pass explicit-public-url

reset
INGRESS_HOST="hub.example.com"
INGRESS_TLS_SECRET="ui-tls"
hub_install_prepare || fail "tls ingress should derive https"
[[ "${HUB_PUBLIC_URL}" == "https://hub.example.com" ]] || fail "tls url: ${HUB_PUBLIC_URL}"
pass tls-public-url

reset
HUB_PUBLIC_URL="https://hub.example.com"
HUB_GITHUB_ORG="percona-cloud"
hub_install_prepare || fail "github org should prepare"
[[ "${HUB_GITHUB_SECRET}" == "hub-github" ]] || fail "default secret: ${HUB_GITHUB_SECRET}"
joined | grep -q 'adapter.hub.github.org=percona-cloud' || fail "org set"
joined | grep -q 'adapter.hub.github.existingSecret=hub-github' || fail "secret set"
joined | grep -q 'adapter.hub.gitDir=' && fail "github path also set gitDir"
pass github-org

reset
HUB_PUBLIC_URL="https://hub.example.com"
HUB_GITHUB_ORG="percona-cloud"
HUB_GITHUB_SECRET="custom-github"
hub_install_prepare || fail "custom secret should prepare"
joined | grep -q 'adapter.hub.github.existingSecret=custom-github' || fail "custom secret"
pass github-secret

reset
HUB_PUBLIC_URL="https://hub.example.com"
HUB_GITHUB_SECRET="custom-github"
if hub_install_prepare 2>/dev/null; then
  fail "secret without org should fail"
fi
pass secret-without-org

reset
HUB_PUBLIC_URL="https://hub.example.com"
HUB_GITHUB_ORG="percona-cloud"
HUB_GIT_URL="https://git.example/hub.git"
if hub_install_prepare 2>/dev/null; then
  fail "github org and git url together should fail"
fi
pass github-and-git-url

reset
HUB_PUBLIC_URL="https://hub.example.com"
HUB_GIT_URL="https://git.example/hub.git"
HUB_CLUSTER_REGION="us-east-1"
hub_install_prepare || fail "git url should prepare"
joined | grep -q 'adapter.hub.gitURL=https://git.example/hub.git' || fail "git url"
joined | grep -q 'adapter.hub.clusterRegion=us-east-1' || fail "region"
joined | grep -q 'adapter.hub.gitDir=/var/lib/hub-git' || fail "git url still needs gitDir"
pass git-url-and-region

reset
HUB_PUBLIC_URL="https://hub.example.com/has space"
if hub_install_prepare 2>/dev/null; then
  fail "a public URL with a space should fail"
fi
pass public-url-token

echo "all hub-install tests passed"
