#!/usr/bin/env bash
#
# Fails when a tracked file spells a product name the repository no longer uses.
# The product is Percona Database Service, short form PDS, and the identifiers
# derived from it are pds, @pds/ui, charts/pds and pds.percona.com.
#
# It also fails when code, an API, a setting or a chart value calls a connected
# environment a cluster. A Kubernetes cluster connected to PDS is an
# environment; cluster stays for the infrastructure under it and for
# Kubernetes' own terms. See the Environment naming rules in AGENTS.md.
#
#   scripts/check-naming.sh          check the repository
#   scripts/check-naming.sh PATH     check another checkout, which is what the
#                                    test does with its fixtures
#
# The word tenant is banned too. An Organization owns Teams and Users, and a
# tenant is what the release before Organizations called its one owner. Nothing
# excuses it on a line: the marker below is for the names above, not for this
# one. The files that may still say it are listed under TENANT_FILE.
#
# Three ways a legacy spelling is still legitimate:
#
#   * It names something outside this repository that was not renamed: the
#     GitHub repository, the published adapter and UI image names, and the
#     GitHub org the hub reads. The published chart is not one of them: helm
#     names the artifact after the chart, so renaming the chart renames it. Those are matched and skipped below.
#   * It is a compat window: the adapter reads what an older release wrote.
#     Put `allow-legacy-name` in a comment on that line.
#   * It is prose that has to quote the old names: the naming rules in
#     AGENTS.md, and this lint and its test. Those files are listed below.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${1:-${REPO_ROOT}}"

# Legacy spellings, as one extended regular expression. Case and separator
# both vary: an env var shouts PERCONA_SERVICES_CHART, a Go identifier spells
# it BasecampTicket, and prose writes Percona Cloud.
LEGACY='[Pp]ercona[-_ ]?[Ss]ervices|PERCONA[-_ ]?SERVICES|[Bb]ase[Cc]amp|BASECAMP|[Pp]ercona[-_ ][Cc]loud|PERCONA[-_ ]CLOUD|PerconaCloud|services\.percona\.com'

# Spellings that name something outside this repository that was not renamed.
# These are cut out of the line before it is judged, so a second, unrelated
# legacy spelling on the same line is still caught.
ALLOWED_TOKEN='percona/basecamp|ghcr\.io/percona/percona-services|percona-cloud|PERCONA_CLOUD|percona_cloud'

# A line the author has marked as reading state written before the rename.
ALLOWED_LINE='allow-legacy-name'

# What an environment used to be called in a route, a query parameter, a stored
# name, a label or a chart value. Each is the old spelling of a name that has a
# current one, so a hit is a leftover unless the line reads old state.
STALE_ENVIRONMENT='/api/(internal/)?clusters([/?" `]|$)|[?&]cluster=|hub_clusters|pds\.percona\.com/cluster|adapter\.hub\.(clusterName|agentClusters)|HUB_AGENT_CLUSTERS|json:"clusters?[",]'

# Go and TypeScript identifiers that carry Cluster or Clusters as a word. The
# ones below name the infrastructure or an outside system and are let through:
# Kubernetes terms, OpenEverest's own cluster, the access value that means
# reachable only inside the Kubernetes cluster, the topology of a database,
# and the copy lint that bans the bare word.
IDENTIFIER_FILES='\.(go|ts|tsx)$'
CLUSTER_IDENTIFIER='\b[A-Za-z0-9_]*([a-z0-9_]Clusters?|Cluster(s[A-Za-z0-9_]|[A-Za-rt-z0-9_]))[A-Za-z0-9_]*|\b(cluster|clusters)[A-Z_][A-Za-z0-9_]*'
ALLOWED_IDENTIFIER='InCluster|inCluster|ClusterIP|ClusterRole|ClusterConfig|PerconaPGCluster|ClusterAccess|ClusterEvent|clusterEvent|ClusterRegion|clusterRegion|ClusterType|clusterType|EverestCluster|DefaultCluster|TopologyCluster|VisibilityCluster|PMMAccessCluster|LegacyClusters|bareCluster|clusterWord|collectClusterLabels|DefaultsTheClusterName|XtraDBCluster|PostgresCluster|pgCluster|BootstrapClusterCreatorAdminPermissions'

# Files whose subject is the rename itself.
ALLOWED_FILE='^(AGENTS\.md|scripts/check-naming\.sh|scripts/check-naming_test\.sh|package-lock\.json)$'

# Files that exist to read or answer the old names for one release. The names
# inside them are the point.
ALLOWED_ENVIRONMENT_FILE='(^|/)legacy_names(_test)?\.go$|^charts/pds/templates/_helpers\.tpl$|^scripts/chart-snapshot\.sh$'

# The word tenant, in any case and as a part of a longer name.
TENANT='[Tt]enant|TENANT'

# The files that may say tenant, because the word is their subject: the upgrade
# from the release before Organizations, its tests and the schema they start
# from, the file that says where
# that upgrade lives and the glossary that names the term it replaced.
TENANT_FILE='adapter/AGENTS\.md|adapter/internal/store/legacy_tenant(_test)?\.go|adapter/internal/store/testdata/release_before_organizations\.sql|CONTEXT\.md|package-lock\.json|scripts/check-naming(_test)?\.sh|AGENTS\.md'

# Prose has to be able to quote the old names, so markdown is not scanned for
# them. Identifiers in code are.
PROSE_FILES='\.md$'

# An enumeration that fails must not read as a clean tree. git answers nothing
# on a checkout that did not land or a safe.directory refusal, and a lint that
# reports success there is worse than no lint.
FILES="$(git -c core.quotePath=false ls-files -c)" || {
  echo "could not list the tracked files. Is this a git checkout?" >&2
  exit 2
}
if [ -z "${FILES}" ]; then
  echo "no tracked files to check. Is this a git checkout?" >&2
  exit 2
fi

status=0

# Paths carry the name too, and a reintroduced charts/percona-services would
# otherwise pass whenever its files happen not to name themselves.
paths="$(printf '%s\n' "${FILES}" | grep -E "${LEGACY}" | grep -vE "${ALLOWED_FILE}" || true)"
if [ -n "${paths}" ]; then
  printf '%s\n' "${paths}" | while IFS= read -r path; do
    printf '%s: the path itself carries a legacy name\n' "${path}"
  done
  status=1
fi

while IFS= read -r file; do
  [ -f "${file}" ] || continue
  if printf '%s' "${file}" | grep -qE "${ALLOWED_FILE}"; then
    continue
  fi
  if file --mime -- "${file}" | grep -q 'charset=binary'; then
    continue
  fi

  # The allowed tokens are cut out before the decision, so a second legacy
  # spelling beside one of them is still caught, but what is printed is the
  # line as the file has it, which is what someone will search for.
  numbers="$(grep -nE "${LEGACY}" "${file}" \
    | grep -vE "${ALLOWED_LINE}" \
    | sed -E "s@${ALLOWED_TOKEN}@@g" \
    | grep -E "${LEGACY}" \
    | cut -d: -f1 || true)"
  if [ -n "${numbers}" ]; then
    printf '%s\n' "${numbers}" | while IFS= read -r number; do
      printf '%s:%s:%s\n' "${file}" "${number}" "$(sed -n "${number}p" "${file}")"
    done
    status=1
  fi
done < <(printf '%s\n' "${FILES}")

# A connected environment is not a cluster. Both patterns skip the lines marked
# as reading old state and run over every tracked text file.
environment_status=0
while IFS= read -r file; do
  [ -f "${file}" ] || continue
  if printf '%s' "${file}" | grep -qE "${ALLOWED_FILE}|${ALLOWED_ENVIRONMENT_FILE}"; then
    continue
  fi
  if file --mime -- "${file}" | grep -q 'charset=binary'; then
    continue
  fi

  hits=""
  if ! printf '%s' "${file}" | grep -qE "${PROSE_FILES}"; then
    hits="$(grep -nE "${STALE_ENVIRONMENT}" "${file}" | grep -vE "${ALLOWED_LINE}" | cut -d: -f1 || true)"
  fi
  if printf '%s' "${file}" | grep -qE "${IDENTIFIER_FILES}"; then
    named="$(grep -nE "${CLUSTER_IDENTIFIER}" "${file}" | grep -vE "${ALLOWED_LINE}" \
      | while IFS= read -r line; do
          number="${line%%:*}"
          text="${line#*:}"
          if printf '%s' "${text}" | grep -oE "${CLUSTER_IDENTIFIER}" | grep -vqE "${ALLOWED_IDENTIFIER}"; then
            printf '%s\n' "${number}"
          fi
        done || true)"
    hits="$(printf '%s\n%s\n' "${hits}" "${named}" | grep -E '^[0-9]+$' | sort -un || true)"
  fi
  if [ -n "${hits}" ]; then
    printf '%s\n' "${hits}" | while IFS= read -r number; do
      printf '%s:%s:%s\n' "${file}" "${number}" "$(sed -n "${number}p" "${file}")"
    done
    environment_status=1
  fi
done < <(printf '%s\n' "${FILES}")

# The word tenant, in paths and in every tracked text file but the few above.
tenant_paths="$(printf '%s\n' "${FILES}" | grep -E "${TENANT}" | grep -vE "^(${TENANT_FILE})$" || true)"
if [ -n "${tenant_paths}" ]; then
  printf '%s\n' "${tenant_paths}" | while IFS= read -r path; do
    printf '%s: the path itself carries the word tenant\n' "${path}"
  done
  status=1
fi

tenant_lines="$(git -c core.quotePath=false grep -nIE "${TENANT}" | grep -vE "^(${TENANT_FILE}):" || true)"
if [ -n "${tenant_lines}" ]; then
  printf '%s\n' "${tenant_lines}"
  cat >&2 <<'EOF'

The word tenant is gone from this repository. A customer is an Organization with
Teams and Users, and a password login is a username. Rename the identifier, the
setting or the sentence. An allow-legacy-name marker does not excuse it.
EOF
  status=1
fi

if [ "${environment_status}" -ne 0 ]; then
  cat >&2 <<'EOF'

A cluster connected to PDS is an environment. Rename the route, field, setting
or identifier. If the line means the Kubernetes cluster under an environment, or
reads what an older release wrote, mark it allow-legacy-name, or name the
infrastructure outright (Kubernetes cluster, EKS cluster).
EOF
  status=1
fi

if [ "${status}" -ne 0 ]; then
  cat >&2 <<'EOF'

The product is Percona Database Service, short form PDS. Use pds, @pds/ui,
charts/pds and pds.percona.com. If the line names something that genuinely was
not renamed, or reads what an older release wrote, mark it allow-legacy-name.
EOF
  exit 1
fi

echo "naming is clean"
