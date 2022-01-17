#!/usr/bin/env bash
set -euo pipefail

(
  exec redis-server
) 2>&1 >/tmp/redis-server.log &

exec reflex -sv go run main.go