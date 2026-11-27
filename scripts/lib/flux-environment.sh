#!/usr/bin/env bash
#
# Connects the lab's second kind cluster the way the control plane connects a
# managed environment, instead of through the join script: Flux, the rendered
# pds HelmRelease in Git, the agent's Secret and the Flux sources. Only AWS is
# left out. Sourced by scripts/lib/lab.sh when LAB_CONNECT=flux.
#
# The release installs the chart from a registry. The lab pushes this
# checkout's chart to a throwaway one on the kind network, so the suite runs
# the chart under review and not the published one.
#
# shellcheck shell=bash

FLUX_CHART_REGISTRY="${FLUX_CHART_REGISTRY:-lab-charts}"
FLUX_CHART_REGISTRY_PORT="${FLUX_CHART_REGISTRY_PORT:-5001}"

# The chart's version, which the release asks the registry for.
flux_environment_chart_version() {
  awk '$1 == "version:" { print $2; exit }' "$1/Chart.yaml"
}

# Points a rendered chart source at another registry that speaks plain HTTP.
# Safe to run twice.
flux_environment_chart_source() {
  local file="$1" url="$2"
  grep -q '^  url: ' "${file}" || {
    echo "${file} has no chart source url" >&2
    return 1
  }
  awk -v url="${url}" '
    /^  insecure: / { next }
    /^  url: /      { print "  url: " url; print "  insecure: true"; next }
    { print }
  ' "${file}" >"${file}.new"
  mv "${file}.new" "${file}"
}

# Starts the registry, joins it to the kind network, and pushes the chart.
flux_environment_push_chart() {
  local chart_dir="$1" work="$2" version
  version="$(flux_environment_chart_version "${chart_dir}")"
  docker rm -f "${FLUX_CHART_REGISTRY}" >/dev/null 2>&1 || true
  docker run -d --name "${FLUX_CHART_REGISTRY}" \
    -p "127.0.0.1:${FLUX_CHART_REGISTRY_PORT}:5000" registry:2 >/dev/null
  docker network connect kind "${FLUX_CHART_REGISTRY}"
  helm package "${chart_dir}" --version "${version}" --destination "${work}" >/dev/null
  helm push "${work}/pds-${version}.tgz" \
    "oci://127.0.0.1:${FLUX_CHART_REGISTRY_PORT}/charts" --plain-http
}

# flux_environment_connect SPOKE STATE GIT_DIR CHART_DIR JOIN_TOKEN HUB_URL UI_PORT
# JOIN_TOKEN is the invitation the hub minted. HUB_URL is the control plane as
# the environment reaches it, and UI_PORT is where this host reaches it.
flux_environment_connect() {
  local spoke="$1" state="$2" git_dir="$3" chart_dir="$4" token="$5" hub_url="$6" ui_port="$7"
  local work="${state}/flux-environment" kube="${state}/lab-spoke.kubeconfig" version
  local context="kind-${spoke}" header
  rm -rf "${work}"
  mkdir -p "${work}/release"
  kind export kubeconfig --name "${spoke}" --kubeconfig "${kube}"

  version="$(flux_environment_chart_version "${chart_dir}")"
  flux_environment_push_chart "${chart_dir}" "${work}"

  # The control plane calls an environment with a short-lived token and an
  # address, not a kubeconfig, so the lab does the same.
  kubectl --context "${context}" -n kube-system create serviceaccount smoke-bootstrap >/dev/null
  kubectl --context "${context}" create clusterrolebinding smoke-bootstrap \
    --clusterrole=cluster-admin --serviceaccount=kube-system:smoke-bootstrap >/dev/null
  kubectl --context "${context}" -n kube-system create token smoke-bootstrap --duration 1h >"${work}/token"
  kubectl --kubeconfig "${kube}" config view --minify --raw \
    -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d >"${work}/ca.crt"

  header="${work}/enroll-header"
  printf 'Authorization: Bearer %s' "${token}" >"${header}"
  curl -fsS -H @"${header}" "http://127.0.0.1:${ui_port}/api/internal/connect/enrollment" >"${work}/enrollment.json"
  rm -f "${header}"

  (cd "$(lab_root)/adapter" && go build -o "${work}/smoke-environment" ./cmd/smoke-environment)
  "${work}/smoke-environment" \
    --endpoint "$(kubectl --kubeconfig "${kube}" config view --minify -o jsonpath='{.clusters[0].cluster.server}')" \
    --ca-file "${work}/ca.crt" --token-file "${work}/token" --enrollment "${work}/enrollment.json" \
    --git-dir "${work}/release" --hub-url "${hub_url}" \
    --image-repository "${ADAPTER_IMAGE}" --image-tag "${TAG}" --chart-version "${version}"

  # The sync loop publishes whatever it finds in the Git tree, so the chart
  # source is changed before the files are copied there.
  flux_environment_chart_source "${work}/release/clusters/${spoke}/platform/pds/helmrepository.yaml" \
    "oci://${FLUX_CHART_REGISTRY}:5000/charts"
  cp -R "${work}/release/." "${git_dir}/"
  rm -f "${work}/token"
}

# Waits for what Flux was told to apply and for the release it installs.
flux_environment_wait() {
  local context="$1" timeout="${2:-1500}"
  lab_wait_kustomization "${context}" instances 600
  lab_wait_kustomization "${context}" pds 900
  kubectl --context "${context}" -n flux-system wait helmrelease/pds \
    --for=condition=Ready --timeout="${timeout}s"
}
