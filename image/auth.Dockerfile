FROM node:20-slim

# node:20-slim ships a `node` user at uid 1000. Reuse it instead of creating
# a duplicate — we just need a non-root user at the same uid the session
# image runs as (1000), so the bind-mounted /creds dir is writable when the
# host user is also uid 1000 (the common Linux case; macOS Docker Desktop
# translates ownership transparently).
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    CLAUDE_CONFIG_DIR=/creds \
    HOME=/home/node

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      tini \
 && rm -rf /var/lib/apt/lists/*

ARG CLAUDE_CODE_VERSION=latest
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

RUN mkdir -p /creds && chown -R node:node /creds

USER node
WORKDIR /home/node

ENTRYPOINT ["/usr/bin/tini", "--", "claude"]
CMD ["auth", "login"]
