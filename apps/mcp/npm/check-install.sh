#!/usr/bin/env bash
#
# Installs the packed package into a clean directory and runs it through npx, the way a person
# does, on the machine this script runs on.
#
#   check-install.sh <directory of the tarball> <expected version>
#
# The registry does not hold this version yet, so the package comes from the tarball. It carries
# the binaries of every platform, and its launcher picks the one for this machine.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "Usage: check-install.sh <directory of the tarball> <expected version>" >&2
  exit 2
fi

tarballs="$(cd "$1" && pwd)"
expected="$2"

os="$(node -p 'process.platform')"
cpu="$(node -p 'process.arch')"
tarball="$(ls "${tarballs}"/gas-lab-pds-mcp-[0-9]*.tgz)"

clean="$(mktemp -d)"
trap 'rm -rf "${clean}"' EXIT
cd "${clean}"
echo '{}' >package.json

npm install --no-audit --no-fund --loglevel=error "${tarball}"

actual="$(npx --no-install pds-mcp version)"
if [[ "${actual}" != "pds-mcp ${expected}" ]]; then
  echo "npx pds-mcp version printed \"${actual}\", expected \"pds-mcp ${expected}\"" >&2
  exit 1
fi
echo "${actual} runs through npx on ${os} ${cpu}"
