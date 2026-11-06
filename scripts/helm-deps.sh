#!/usr/bin/env bash
#
# Fetch the OpenEverest, provider, and PMM chart tarballs Chart.yaml declares.
#
#   scripts/helm-deps.sh
#
# Required before helm lint, helm template, or helm upgrade of the umbrella.
# OpenEverest 2.0.0-dev.4 is a prerelease; the pin in Chart.yaml is enough
# for helm dependency update. The PXC and PostgreSQL providers are OCI charts.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHART_DIR="${REPO_ROOT}/charts/pds"

helm repo add openeverest https://openeverest.github.io/helm-charts/ >/dev/null
helm repo add percona https://percona.github.io/percona-helm-charts/ >/dev/null
helm repo update openeverest percona >/dev/null
helm dependency update "${CHART_DIR}"
