#!/usr/bin/env bash
#
# Patches the OpenEverest chart tarball helm-deps.sh fetches.
#
#   source scripts/lib/openeverest-patch.sh
#   patch_openeverest_accounts charts/pds/charts/openeverest-<version>.tgz
#
# Secret everest-accounts carries helm.sh/resource-policy: keep, so it outlives
# an uninstall. On the next install the template copies every annotation of the
# kept Secret and then writes the policy again, and a manifest with the key
# twice fails to render, so the release can never be installed again. The
# patch leaves the policy out of the copy. It stops the fetch when the template
# no longer looks the way it expects, so a new OpenEverest version is checked
# by hand: drop the patch when upstream renders the Secret correctly.

openeverest_accounts_path="openeverest/templates/everest-server/accounts.secret.yaml"
openeverest_accounts_copy='  {{- range $key, $value := $secret.metadata.annotations }}'
openeverest_accounts_skip='  {{- if ne $key "helm.sh/resource-policy" }}'

patch_openeverest_accounts() {
  local tgz="$1" work template
  work="$(mktemp -d)"
  tar -xzf "${tgz}" -C "${work}"
  template="${work}/${openeverest_accounts_path}"
  if [[ ! -f "${template}" ]]; then
    rm -rf "${work}"
    echo "openeverest-patch: ${tgz} has no accounts template at ${openeverest_accounts_path}; check whether the patch is still needed" >&2
    return 1
  fi
  if grep -qF "${openeverest_accounts_skip}" "${template}"; then
    rm -rf "${work}"
    return 0
  fi
  if [[ "$(grep -cF "${openeverest_accounts_copy}" "${template}")" != 1 ]]; then
    rm -rf "${work}"
    echo "openeverest-patch: the accounts template in ${tgz} changed; check whether upstream fixed the duplicate helm.sh/resource-policy and drop the patch" >&2
    return 1
  fi

  # The copied annotation and its closing line gain a condition that skips the policy.
  awk -v copy="${openeverest_accounts_copy}" -v skip="${openeverest_accounts_skip}" '
    in_copy && /^  \{\{- end \}\}$/ { print "  {{- end }}"; in_copy = 0 }
    { print }
    $0 == copy { print skip; in_copy = 1 }
  ' "${template}" >"${template}.patched"
  mv "${template}.patched" "${template}"
  # macOS tar adds AppleDouble ._ entries unless told not to, and Helm refuses
  # a chart archive with content outside its directory.
  COPYFILE_DISABLE=1 tar -czf "${tgz}" -C "${work}" openeverest
  rm -rf "${work}"
}
