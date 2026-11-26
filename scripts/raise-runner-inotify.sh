#!/usr/bin/env bash
#
# Raise the inotify limits on a GitHub-hosted Ubuntu runner before a kind job.
#
#   scripts/raise-runner-inotify.sh
#
# Every kind node runs systemd, containerd and a kubelet, and all of them take
# inotify instances from the runner's one user. The Ubuntu 26.04 runner keeps
# the kernel default of 128 instances, which six nodes exhaust: the last
# kubelets to start never report healthy and kubeadm join times out after
# four minutes. 8192 instances and 1048576 watches leave room for every lab.
set -euo pipefail

sysctl fs.inotify.max_user_instances fs.inotify.max_user_watches
sudo sysctl -w fs.inotify.max_user_instances=8192
sudo sysctl -w fs.inotify.max_user_watches=1048576
