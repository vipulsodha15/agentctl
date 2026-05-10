#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="${REPO_ROOT:-/work}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

export AGENTCTL_HOME="${AGENTCTL_HOME:-$HOME}"

echo "[e2e] waiting for Docker daemon at ${DOCKER_HOST:-unix:///var/run/docker.sock}"
for _ in $(seq 1 60); do
  if docker info >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker info >/dev/null

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  if [[ -n "${ANTHOPIC_KEY:-}" ]]; then
    export ANTHROPIC_API_KEY="$ANTHOPIC_KEY"
  fi
fi
if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "[e2e] ANTHROPIC_API_KEY (or ANTHOPIC_KEY) is required" >&2
  exit 64
fi

echo "[e2e] building agentctl"
( cd "$REPO_ROOT" && go build -o "$INSTALL_DIR/agentctl" ./cmd/agentctl )
ln -sf "$INSTALL_DIR/agentctl" "$INSTALL_DIR/agentd"

echo "[e2e] laying down image build context"
mkdir -p "$AGENTCTL_HOME/.local/share/agentctl/image"
cp -R "$REPO_ROOT/image/." "$AGENTCTL_HOME/.local/share/agentctl/image/"

echo "[e2e] laying down built-in skills"
mkdir -p "$AGENTCTL_HOME/.local/share/agentctl/builtin-skills"
cp -R "$REPO_ROOT/builtin-skills/." "$AGENTCTL_HOME/.local/share/agentctl/builtin-skills/"

echo "[e2e] running scenarios"
for scenario in "$REPO_ROOT"/test/scenarios/*.sh; do
  echo ""
  echo "=== $(basename "$scenario") ==="
  bash "$scenario"
done

echo ""
echo "[e2e] all scenarios passed"
