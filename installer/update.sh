#!/usr/bin/env bash
# v1 update: pull the latest changes on the current branch, rebuild the
# agentctl binary + bundled assets via install.sh, and restart the
# agentd service so it picks up the new binary.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
SKIP_PULL="${SKIP_PULL:-0}"
SKIP_RESTART="${SKIP_RESTART:-0}"

usage() {
  cat <<EOT
Usage: bash installer/update.sh

  Pulls the latest changes on the current branch, rebuilds the agentctl
  binary, refreshes the image build context and bundled built-in skills,
  and restarts the agentd service.

  Env:
    INSTALL_DIR          install dir for the binary (default ~/.local/bin)
    AGENTCTL_DATA_DIR    XDG data dir override (default ~/.local/share/agentctl)
    SKIP_PULL=1          skip 'git pull' (use the current working tree as-is)
    SKIP_RESTART=1       skip the agentd service restart at the end
    REMOTE               remote to pull from when no upstream is set (default origin)
EOT
}

for arg in "$@"; do
  case "$arg" in
    -h|--help) usage; exit 0 ;;
    *) echo "update.sh: unknown arg $arg" >&2; usage; exit 64 ;;
  esac
done

cd "$REPO_ROOT"

if [[ "$SKIP_PULL" != "1" ]]; then
  if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "update.sh: $REPO_ROOT is not a git checkout; cannot pull. Re-run with SKIP_PULL=1 to skip." >&2
    exit 2
  fi
  branch="$(git symbolic-ref --quiet --short HEAD || true)"
  if [[ -z "$branch" ]]; then
    echo "update.sh: detached HEAD; refusing to pull. Check out a branch or re-run with SKIP_PULL=1." >&2
    exit 2
  fi
  if upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null)"; then
    echo "Pulling latest changes on '$branch' (tracking $upstream) ..."
    git pull --ff-only
  else
    remote="${REMOTE:-origin}"
    if ! git remote get-url "$remote" >/dev/null 2>&1; then
      echo "update.sh: no upstream for '$branch' and remote '$remote' is missing. Set one with 'git branch --set-upstream-to=<remote>/<branch>' or re-run with SKIP_PULL=1." >&2
      exit 2
    fi
    echo "Pulling latest changes on '$branch' from $remote (no upstream configured) ..."
    git pull --ff-only "$remote" "$branch"
  fi
else
  echo "Skipping git pull (SKIP_PULL=1)."
fi

echo "Running installer ..."
INSTALL_DIR="$INSTALL_DIR" bash "$SCRIPT_DIR/install.sh"

if [[ "$SKIP_RESTART" == "1" ]]; then
  echo "Skipping agentd restart (SKIP_RESTART=1)."
  exit 0
fi

AGENTCTL_BIN="$INSTALL_DIR/agentctl"
if [[ ! -x "$AGENTCTL_BIN" ]]; then
  if command -v agentctl >/dev/null 2>&1; then
    AGENTCTL_BIN="$(command -v agentctl)"
  else
    echo "update.sh: agentctl not found on PATH; skipping restart. Run 'agentctl service restart' manually once it is." >&2
    exit 2
  fi
fi

echo "Restarting agentd ..."
"$AGENTCTL_BIN" service restart
