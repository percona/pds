#!/usr/bin/env bash
#
# Lays out the one package from what GoReleaser built, and packs it.
#
#   pack.sh <release tag> <directory for the tarball>
#
# Run it from apps/mcp after `goreleaser release`. The version comes from the tag, the same
# way it reached the binaries through PDS_MCP_VERSION. It prints the version.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "Usage: pack.sh <release tag> <directory for the tarball>" >&2
  exit 2
fi

tag="$1"
tarballs="$2"

version="$(node npm/release.mjs version "${tag}")"
rm -rf dist/npm
mkdir -p "${tarballs}"
tarballs="$(cd "${tarballs}" && pwd)"

dir="$(node npm/release.mjs build "${tag}" dist/goreleaser/artifacts.json dist/npm)"
(cd "${dir}" && npm pack --pack-destination "${tarballs}" --loglevel=error >/dev/null)

echo "${version}"
