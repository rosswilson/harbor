#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
# Stop reconciliation before removing only containers owned by this lab.
docker compose stop controlplane
ids=$(docker ps -aq --filter label=harbor.owner=harbor-lab)
if [ -n "$ids" ]; then
  printf '%s\n' "$ids" | while IFS= read -r id; do
    docker rm -f "$id"
  done
fi
if [ "${1:-}" = "--reset" ]; then
  docker compose down -v
else
  docker compose down
fi
