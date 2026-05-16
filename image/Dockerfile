FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PATH=/opt/agentctl/venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    HOME=/home/agent \
    XDG_CONFIG_HOME=/work/.config \
    XDG_CACHE_HOME=/work/.cache \
    GIT_TERMINAL_PROMPT=0

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      openssh-client \
      jq \
      tini \
      gosu \
      build-essential \
      pkg-config \
 && rm -rf /var/lib/apt/lists/*

# GitHub CLI (`gh`) so agents can use `gh pr create`, `gh issue`, etc. The
# container already gets a $GITHUB_TOKEN injected by agentd; `gh` picks that
# up automatically (no `gh auth login` required). `gh` subcommands that shell
# out to git (e.g. `gh repo clone`, `gh pr create`'s push) rely on the
# credential helper written under $XDG_CONFIG_HOME/git/ by the entrypoint.
RUN install -d -m 0755 /usr/share/keyrings \
 && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends gh \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get update \
 && apt-get install -y --no-install-recommends nodejs python3 python3-pip python3-venv \
 && rm -rf /var/lib/apt/lists/*

ARG CLAUDE_AGENT_SDK_VERSION=0.1.80
RUN python3 -m venv /opt/agentctl/venv \
 && /opt/agentctl/venv/bin/pip install --no-cache-dir --upgrade pip \
 && /opt/agentctl/venv/bin/pip install --no-cache-dir \
      "claude-agent-sdk==${CLAUDE_AGENT_SDK_VERSION}" \
      watchdog

# OpenAI Codex CLI — second agent runtime per ADR 0020 §1.6. Installed
# globally so the `codex` binary lands on PATH for the `agent` user (uid
# 1000) created below. `--omit=optional` and `npm cache clean --force`
# keep the layer within ADR 0014's ~50–100MB delta budget.
# As of 2026-05-16, @openai/codex 0.130.0 is the current release on npm.
ARG CODEX_CLI_VERSION=0.130.0
RUN npm install -g --omit=optional "@openai/codex@${CODEX_CLI_VERSION}" \
 && npm cache clean --force

COPY shim/ /opt/agentctl/shim/
RUN /opt/agentctl/venv/bin/pip install --no-cache-dir -r /opt/agentctl/shim/requirements.txt

COPY entrypoint /usr/local/bin/agentctl-entrypoint
COPY config-templates/ /etc/agentctl/templates/
RUN useradd --create-home --uid 1000 --shell /bin/bash agent \
 && mkdir -p /work /skills /run/agentctl/control \
 && chown -R agent:agent /work /skills /home/agent /opt/agentctl \
 && chmod 0755 /run/agentctl/control \
 && chmod +x /usr/local/bin/agentctl-entrypoint

ENV PYTHONPATH=/opt/agentctl

USER agent
WORKDIR /work

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/agentctl-entrypoint"]
