#!/usr/bin/env bash
#
# bounded runs an executable and kills it after a number of seconds. kubectl
# exec has no deadline of its own, and neither timeout nor gtimeout is on
# every machine. Sourced by scripts/hub-smoke.sh and covered by
# scripts/lib/bounded_test.sh.
#
# The command must be an executable, not a shell function: the watchdog kills
# the process it started, and a function would run in a subshell whose child
# is the real command.
#
# The watchdog waits with the shell's own read timeout on a pipe nobody
# writes, so it has no child process. Killing it, which happens whenever the
# command finishes first, therefore cannot leave a sleep behind, however soon
# after it started. Its output is closed so a caller capturing the output
# with $(...) is not held open by it.
#
# shellcheck shell=bash

bounded() {
  local secs="$1" pid watcher rc=0 dir
  shift
  dir="$(mktemp -d)"
  mkfifo "${dir}/idle"
  "$@" &
  pid=$!
  (
    read -r -t "${secs}" <>"${dir}/idle" || true
    kill "${pid}" 2>/dev/null
  ) >/dev/null 2>&1 &
  watcher=$!
  # The braces catch the shell's own "Terminated" notice for a killed job.
  { wait "${pid}" || rc=$?; } 2>/dev/null
  { kill "${watcher}" && wait "${watcher}"; } 2>/dev/null || true
  rm -rf "${dir}"
  return "${rc}"
}
