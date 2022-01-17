#!/usr/bin/env bash
set -euo pipefail

if [ "${REDIS_URL:-}" = "" ]; then
  echo "starting redis-server"
  (
    exec redis-server
  ) 2>&1 >/tmp/redis-server.log &
fi

exec reflex -sv go run main.go