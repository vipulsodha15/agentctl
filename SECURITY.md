# Security Policy

## Reporting a vulnerability

If you believe you've found a security vulnerability in agentctl, please
report it privately. **Do not** open a public GitHub issue.

Use GitHub's private vulnerability reporting:

> Go to the repo → **Security** → **Report a vulnerability**

…or email the maintainer directly (address in the GitHub profile of the
copyright holder listed in [`NOTICE`](NOTICE)).

Please include:

- A description of the issue and its impact
- Steps to reproduce, or a proof-of-concept
- The version (`agentctl version`) and OS where you reproduced it
- Whether the issue is already public (e.g. discussed on a forum)

You should receive an acknowledgement within **5 business days**. We aim to
ship a fix or a mitigation plan within **30 days** for high-severity issues.

## Scope

In scope:

- The `agentctl` CLI and `agentd` daemon
- The session base image (`image/Dockerfile`, runtime shim)
- The Web UI served by `agentd`
- The installer (`installer/install.sh`)

Out of scope (please report to the upstream project instead):

- Vulnerabilities in the Claude Agent SDK, OpenAI SDKs, or other third-party
  packages — report those to their respective maintainers.
- Vulnerabilities in Docker itself.
- Vulnerabilities in MCP servers you've registered yourself.

## Supported versions

agentctl is pre-1.0; only the latest `main` and the most recent tagged
release receive security fixes.

## Hardening notes

agentctl runs untrusted agent output inside Docker containers on dedicated
bridge networks. A few notes for operators:

- Each session container has access to the credentials you've configured
  (Anthropic OAuth, API keys, MCP server tokens). Treat the session base
  image as sensitive.
- The daemon listens on a Unix socket by default and the Web UI binds to
  `127.0.0.1` only. Do not expose `agentd`'s HTTP port to the network.
- See [`architecture/security.md`](architecture/security.md) for the full
  threat model.
