#!/usr/bin/env bash
# v1 install: build agentctl from this checkout, lay down the image build
# context and the bundled built-in skills under ~/.local/share/agentctl/.
# No CDN, no signing, no system-service install (`agentctl init` does that).

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
DATA_DIR="${AGENTCTL_DATA_DIR:-$HOME/.local/share/agentctl}"
ACTION="install"

usage() {
  cat <<EOT
Usage: bash installer/install.sh [--uninstall]

  Lays down the agentctl binary, the Docker build context, and the
  bundled built-in skills. Run \`agentctl init\` afterwards.

  Env:
    INSTALL_DIR          install dir for the binary (default ~/.local/bin)
    AGENTCTL_DATA_DIR    XDG data dir override (default ~/.local/share/agentctl)
EOT
}

for arg in "$@"; do
  case "$arg" in
    --uninstall) ACTION="uninstall" ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install.sh: unknown arg $arg" >&2; usage; exit 64 ;;
  esac
done

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "install.sh: required command not on PATH: $1" >&2
    exit 2
  fi
}

go_version_ok() {
  local raw
  raw="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
  if [[ -z "$raw" ]]; then
    return 1
  fi
  local major minor
  major="${raw%%.*}"
  minor="${raw#*.}"
  minor="${minor%%.*}"
  if (( major > 1 )); then
    return 0
  fi
  if (( major == 1 && minor >= 23 )); then
    return 0
  fi
  return 1
}

uninstall() {
  if [[ -L "$INSTALL_DIR/agentd" ]]; then
    rm -f "$INSTALL_DIR/agentd"
  fi
  rm -f "$INSTALL_DIR/agentctl"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user disable --now agentd.service >/dev/null 2>&1 || true
  fi
  if command -v launchctl >/dev/null 2>&1; then
    launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.agentctl.agentd.plist" >/dev/null 2>&1 || true
  fi
  echo "agentctl uninstalled."
  echo ""
  echo "User data is left in place:"
  echo "  $HOME/.config/agentctl/"
  echo "  $DATA_DIR/"
  echo "Remove these manually if you want a clean wipe."
}

install() {
  require_cmd go
  require_cmd docker
  if ! go_version_ok; then
    echo "install.sh: Go 1.23+ required (got $(go version 2>/dev/null || echo missing))" >&2
    exit 2
  fi

  mkdir -p "$INSTALL_DIR" "$DATA_DIR/image" "$DATA_DIR/builtin-skills"

  echo "Building agentctl from $REPO_ROOT ..."
  ( cd "$REPO_ROOT" && go build -o "$INSTALL_DIR/agentctl" ./cmd/agentctl )
  ln -sf "$INSTALL_DIR/agentctl" "$INSTALL_DIR/agentd"

  echo "Laying down image build context to $DATA_DIR/image/ ..."
  rm -rf "$DATA_DIR/image"
  cp -R "$REPO_ROOT/image" "$DATA_DIR/image"

  echo "Laying down built-in skills to $DATA_DIR/builtin-skills/ ..."
  rm -rf "$DATA_DIR/builtin-skills"
  cp -R "$REPO_ROOT/builtin-skills" "$DATA_DIR/builtin-skills"

  cat > "$DATA_DIR/install_metadata.json" <<META
{
  "version": "$(${INSTALL_DIR}/agentctl version 2>/dev/null | awk '{print $2}' || echo unknown)",
  "install_method": "install.sh (repo checkout)",
  "installed_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source_url": "$REPO_ROOT",
  "claude_import_offered_at": null,
  "claude_imported_skills": []
}
META

  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      echo ""
      echo "WARN: $INSTALL_DIR is not on PATH. Add this to your shell rc:"
      echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
      ;;
  esac

  echo ""
  echo "agentctl installed at $INSTALL_DIR/agentctl"
  echo "Next: agentctl init"
}

case "$ACTION" in
  install) install ;;
  uninstall) uninstall ;;
esac
