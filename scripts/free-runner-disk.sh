#!/usr/bin/env bash
#
# Free disk on a GitHub-hosted Ubuntu runner before a kind job.
#
#   scripts/free-runner-disk.sh
#
# A kind cluster keeps every image in each node's containerd on the runner's
# disk, and the platform plus the database operators pull several gigabytes.
# Without this the runner fills up: image pulls fail to extract layers,
# docker builds stop with "no space left on device", and in the worst case
# the runner cannot write its own log. The SDKs and tool caches removed here
# are unrelated to the jobs that call this, which install what they need.
set -euo pipefail

df -h /
sudo rm -rf /usr/local/lib/android /usr/share/dotnet /opt/ghc /usr/local/.ghcup \
  /opt/hostedtoolcache /usr/share/swift /usr/local/share/boost /opt/az \
  /usr/lib/jvm /usr/lib/google-cloud-sdk
docker system prune --all --force
df -h /
