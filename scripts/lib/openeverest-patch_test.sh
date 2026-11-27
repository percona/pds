#!/usr/bin/env bash
#
# Unit tests for scripts/lib/openeverest-patch.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/lib/openeverest-patch.sh
source "${REPO_ROOT}/scripts/lib/openeverest-patch.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok $*"; }

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# The accounts template as OpenEverest 2.0.0-dev.4 ships it, password line aside.
upstream_template() {
  cat <<'EOF'
{{- if .Release.IsInstall }}
{{- $secretName := (printf "everest-accounts") -}}
{{- $secret := (lookup "v1" "Secret" (include "everest.namespace" .) $secretName ) -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ $secretName }}
  namespace: {{ include  "everest.namespace" . }}
  annotations:
  {{- if not $secret }}
    insecure-password/admin: "true"
  {{- else }}
  {{- range $key, $value := $secret.metadata.annotations }}
    {{ $key }}: {{ $value | quote }}
  {{- end }}
  {{- end }}
    helm.sh/resource-policy: keep
data:
  users.yaml: dXNlcnM=
{{- end }}
EOF
}

# make_chart writes an openeverest chart tarball whose accounts template is
# the given text, and prints its path.
make_chart() {
  local dir="$1" template="$2"
  mkdir -p "${dir}/src/openeverest/templates/everest-server"
  printf 'apiVersion: v2\nname: openeverest\nversion: 2.0.0-dev.4\n' >"${dir}/src/openeverest/Chart.yaml"
  printf '%s\n' "${template}" >"${dir}/src/openeverest/templates/everest-server/accounts.secret.yaml"
  printf '{{- define "everest.namespace" -}}everest-system{{- end -}}\n' >"${dir}/src/openeverest/templates/_ns.tpl"
  COPYFILE_DISABLE=1 tar -czf "${dir}/openeverest-2.0.0-dev.4.tgz" -C "${dir}/src" openeverest
  echo "${dir}/openeverest-2.0.0-dev.4.tgz"
}

accounts_template() {
  tar -xzOf "$1" openeverest/templates/everest-server/accounts.secret.yaml
}

# --- a kept Secret's resource policy is not copied a second time ---
tgz="$(make_chart "${WORK}/a" "$(upstream_template)")"
patch_openeverest_accounts "${tgz}"
got="$(accounts_template "${tgz}")"
grep -qF '{{- if ne $key "helm.sh/resource-policy" }}' <<<"${got}" \
  || fail "the copy of the kept Secret's annotations still includes helm.sh/resource-policy"
[[ "$(grep -c 'helm.sh/resource-policy: keep' <<<"${got}")" == 1 ]] \
  || fail "the template must still write helm.sh/resource-policy: keep once"
pass "a kept Secret's resource policy is not copied a second time"

# --- the patched tarball still renders a fresh install ---
if command -v helm >/dev/null; then
  out="$(helm template t "${tgz}" --show-only templates/everest-server/accounts.secret.yaml)" \
    || fail "helm cannot render the patched tarball"
  [[ "$(grep -c 'helm.sh/resource-policy' <<<"${out}")" == 1 ]] || fail "a fresh install renders the resource policy once"
  pass "the patched tarball still renders a fresh install"
fi

# --- patching twice changes nothing ---
before="$(accounts_template "${tgz}")"
patch_openeverest_accounts "${tgz}"
[[ "$(accounts_template "${tgz}")" == "${before}" ]] || fail "a second patch changed the template"
pass "patching twice changes nothing"

# --- a template it does not recognise stops the fetch ---
tgz="$(make_chart "${WORK}/b" "apiVersion: v1
kind: Secret")"
if patch_openeverest_accounts "${tgz}" 2>"${WORK}/err"; then
  fail "an unrecognised accounts template must fail"
fi
grep -q "accounts template" "${WORK}/err" || fail "the failure must name the accounts template"
pass "a template it does not recognise stops the fetch"
