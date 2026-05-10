FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PATH=/opt/agentctl/venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      python3 \
      python3-venv \
      python3-pip \
      jq \
      tini \
 && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/agentctl/venv \
 && /opt/agentctl/venv/bin/pip install --no-cache-dir --upgrade pip \
 && /opt/agentctl/venv/bin/pip install --no-cache-dir claude-agent-sdk==0.1.80

RUN useradd --uid 1000 --create-home --shell /bin/bash agent \
 && mkdir -p /work /skills /run/agentctl/control /opt/agentctl/shim \
 && chown -R agent:agent /work

COPY shim/ /opt/agentctl/shim/
COPY entrypoint /usr/local/bin/agentctl-entrypoint
RUN chmod +x /usr/local/bin/agentctl-entrypoint

USER agent
WORKDIR /work

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/agentctl-entrypoint"]
