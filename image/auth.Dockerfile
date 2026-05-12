FROM node:20-slim

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    CLAUDE_CONFIG_DIR=/creds \
    HOME=/home/agent

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      tini \
 && rm -rf /var/lib/apt/lists/*

ARG CLAUDE_CODE_VERSION=latest
RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

RUN useradd --create-home --uid 1000 --shell /bin/bash agent \
 && mkdir -p /creds \
 && chown -R agent:agent /creds /home/agent

USER agent
WORKDIR /home/agent

ENTRYPOINT ["/usr/bin/tini", "--", "claude"]
CMD ["auth", "login"]
