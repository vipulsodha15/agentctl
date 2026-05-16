FROM node:20-slim

# node:20-slim ships a `node` user at uid 1000. Reuse it instead of creating
# a duplicate — we just need a non-root user at the same uid the session
# image runs as (1000), so the bind-mounted /creds dir is writable when the
# host user is also uid 1000 (the common Linux case; macOS Docker Desktop
# translates ownership transparently).
#
# This image bundles both vendor CLIs so a single docker build supports
# either flow. Which CLI runs is decided at `docker run` time by the
# $PROVIDER environment variable; entry.sh below branches on it. ADR 0020
# §5 — keeping it one image avoids forcing the CLI to track two image tags.
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    HOME=/home/node

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      tini \
 && rm -rf /var/lib/apt/lists/*

# Pin both CLIs. The codex pin must match image/Dockerfile's
# CODEX_CLI_VERSION (0.130.0 as of phase 1) so OAuth tokens written by the
# helper are read back by the same codex version inside session containers
# — a token-format drift between minor releases would silently break OAuth.
ARG CLAUDE_CODE_VERSION=latest
ARG CODEX_CLI_VERSION=0.130.0
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm install -g --omit=optional "@openai/codex@${CODEX_CLI_VERSION}" \
 && npm cache clean --force

RUN mkdir -p /creds && chown -R node:node /creds

# entry.sh branches on $PROVIDER. Anthropic path matches the historical
# behaviour byte-for-byte; OpenAI path runs `codex login --device-auth`
# which prints a URL + code (host browser completes the flow). Both write
# their canonical credentials file under /creds, which the host has bind-
# mounted from ~/.config/agentctl/{claude,codex}/.
COPY auth-entry.sh /usr/local/bin/auth-entry.sh
RUN chmod +x /usr/local/bin/auth-entry.sh

USER node
WORKDIR /home/node

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/auth-entry.sh"]
