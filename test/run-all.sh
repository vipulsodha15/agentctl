#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
exec docker compose -f docker-compose.test.yml up --abort-on-container-exit --build
