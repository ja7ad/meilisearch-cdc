#!/usr/bin/env bash
# Runs the e2e service (deploy/e2e/cdc.env) under a tiny supervisor that
# starts it again whenever it exits, like systemd or Kubernetes would. The
# fault tolerance tests kill it with SIGKILL through the pid file:
#
#   kill -9 "$(cat deploy/e2e/.service.pid)"
#
# Remove deploy/e2e/.service.run (or stop this script) to stop for good.
set -uo pipefail
cd "$(dirname "$0")/../.."
set -a && . deploy/e2e/cdc.env && set +a
BIN=${BIN:-./bin/meilisearch-cdc}
touch deploy/e2e/.service.run
trap 'rm -f deploy/e2e/.service.run; kill "$(cat deploy/e2e/.service.pid 2>/dev/null)" 2>/dev/null; exit 0' INT TERM
while [ -f deploy/e2e/.service.run ]; do
  "$BIN" &
  echo $! > deploy/e2e/.service.pid
  wait $!
  echo "service exited with $?; restarting" >&2
  sleep 1
done
