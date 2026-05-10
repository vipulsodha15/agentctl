#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
AGENTCTL_HOME="${AGENTCTL_HOME:-$HOME}"

CFG_DIR="$AGENTCTL_HOME/.config/agentctl"
DATA_DIR="$AGENTCTL_HOME/.local/share/agentctl"

GITHUB_PAT="${GITHUB_PAT_TEST:-test-pat-not-validated}"
if [[ "$GITHUB_PAT" == "test-pat-not-validated" ]]; then
  export AGENTCTL_SKIP_GITHUB_PAT_CHECK=1
fi

echo "[01-init] running first init"
agentctl init \
  --anthropic-key "$ANTHROPIC_API_KEY" \
  --github-pat "$GITHUB_PAT" \
  --no-import-claude-skills \
  --foreground &

INIT_PID=$!
trap 'kill $INIT_PID 2>/dev/null || true' EXIT

# Wait for agentd to come up; init returns once /healthz is reachable.
for _ in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:7777/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "[01-init] verifying secrets.json mode 0600"
test -f "$CFG_DIR/secrets.json"
mode=$(stat -c '%a' "$CFG_DIR/secrets.json" 2>/dev/null || stat -f '%A' "$CFG_DIR/secrets.json")
[[ "$mode" == "600" ]]

echo "[01-init] verifying web_token mode 0600"
test -f "$CFG_DIR/web_token"
mode=$(stat -c '%a' "$CFG_DIR/web_token" 2>/dev/null || stat -f '%A' "$CFG_DIR/web_token")
[[ "$mode" == "600" ]]

echo "[01-init] verifying agentd.db exists"
test -f "$DATA_DIR/agentd.db"

echo "[01-init] /healthz reachable"
curl -fsS http://127.0.0.1:7777/healthz | jq -e '.docker.ok == true'

echo "[01-init] doctor exits 0"
agentctl doctor

echo "[01-init] re-running init (idempotency)"
agentctl init --no-import-claude-skills --foreground >/dev/null

echo "[01-init] OK"
