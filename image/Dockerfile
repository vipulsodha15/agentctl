FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PATH=/opt/agentctl/venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    HOME=/home/agent \
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
