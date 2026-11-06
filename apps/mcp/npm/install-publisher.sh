#!/usr/bin/env bash
#
# Installs mcp-publisher, the MCP Registry's command line tool, after checking its signature.
#
#   install-publisher.sh <directory>
#
# Needs cosign on the path. The directory then holds the mcp-publisher binary. The version is
# pinned here: change it on purpose, and read the registry's release notes first.
set -euo pipefail

version="v1.8.6"

if [[ $# -ne 1 ]]; then
  echo "Usage: install-publisher.sh <directory>" >&2
  exit 2
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
archive="mcp-publisher_${os}_${arch}.tar.gz"
base_url="https://github.com/modelcontextprotocol/registry/releases/download/${version}"

mkdir -p "$1"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

curl --fail --location --silent --show-error --output "${work}/${archive}" "${base_url}/${archive}"
curl --fail --location --silent --show-error --output "${work}/${archive}.sigstore.json" \
  "${base_url}/${archive}.sigstore.json"
cosign verify-blob \
  --bundle "${work}/${archive}.sigstore.json" \
  --certificate-identity "https://github.com/modelcontextprotocol/registry/.github/workflows/release.yml@refs/tags/${version}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${work}/${archive}"
tar -xzf "${work}/${archive}" -C "$1" mcp-publisher
