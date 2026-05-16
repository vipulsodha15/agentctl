#!/usr/bin/env bash
# auth-entry.sh — entrypoint for image/auth.Dockerfile.
#
# Dispatches the OAuth helper container into the right vendor flow based on
# $PROVIDER. The host CLI (`agentctl auth login --provider <p>`) sets the
# variable and bind-mounts the matching credentials directory at /creds:
#
#   PROVIDER=anthropic  → CLAUDE_CONFIG_DIR=/creds, runs `claude auth login`
#                         (writes /creds/.credentials.json).
#   PROVIDER=openai     → CODEX_HOME=/creds, runs `codex login --device-auth`
#                         (writes /creds/auth.json; prints a code+URL the
#                         user enters on the host browser — the Codex
#                         equivalent of Claude's paste fallback).
#
# Defaulting to anthropic preserves backward compatibility for any external
# caller that drove this image before the --provider flag landed.
set -euo pipefail

PROVIDER="${PROVIDER:-anthropic}"

case "$PROVIDER" in
  anthropic)
    export CLAUDE_CONFIG_DIR=/creds
    exec claude auth login "$@"
    ;;
  openai)
    export CODEX_HOME=/creds
    # --device-auth: codex prints a code + URL, user opens on host browser
    # and types the code. Required because the helper container has no
    # reachable callback port (Anthropic's flow has the same constraint).
    # Verified shipping in @openai/codex@0.130.0 (codex-rs/cli LoginCommand
    # — see ADR 0020 §Items to verify and Phase 2 §2.5).
    exec codex login --device-auth "$@"
    ;;
  *)
    echo "auth-entry.sh: unknown PROVIDER=$PROVIDER (expected anthropic|openai)" >&2
    exit 2
    ;;
esac
