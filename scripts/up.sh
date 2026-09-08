#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
docker compose --profile build build
docker compose up -d --wait
printf '\nHarbor UI: http://localhost:9090\nTraffic: http://betaco.localhost:8080\nDefault local API token: harbor-local-dev (unless overridden)\n'
