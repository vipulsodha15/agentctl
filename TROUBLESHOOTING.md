# Troubleshooting

If something is wrong with your install, run

    agentctl doctor

first. It prints the state of every install + connectivity check.
`agentctl doctor --json` is the machine-readable form;
`agentctl doctor --fix` applies known repairs idempotently;
`agentctl doctor --repair-db` runs sqlite `VACUUM` on `agentd.db`.

Below are the most common failure modes (cross-referenced with
`architecture/install-and-update.md` section 6).

## Docker missing or daemon unreachable

**Symptoms.** `agentctl init` exits with `docker info failed` or
`docker not on PATH`. `agentctl start` fails with
`docker.unavailable`. `agentctl doctor` shows
`docker.reachable FAIL`.

**Fix.** Install Docker (Docker Desktop on macOS, Docker Engine on
Linux), start the daemon, and ensure your user is in the `docker`
group:

    # Linux only
    sudo usermod -aG docker "$USER"
    newgrp docker
    docker info       # should print server version

Then re-run `agentctl init`.

## Image build fails (network, disk full)

**Symptoms.** `agentctl init` aborts inside the "Building base
image" phase with a docker build error tail. `agentctl doctor`
shows `image.built FAIL` or `image.present FAIL`. Common causes:
no network during `apt-get install`, full /var/lib/docker, or a
corrupt build context.

**Fix.**

1. Check disk: `df -h /var/lib/docker` and `docker system df`. If
   it's full, `docker system prune --volumes`.
2. Ensure the build context is intact: `ls
   ~/.local/share/agentctl/image/Dockerfile`. If it's missing,
   re-run `bash installer/install.sh`.
3. Re-run `agentctl init` (or `agentctl init --repair`). The build
   resumes from cached layers.
4. To bypass the cache once: `agentctl update --no-cache`.

## Anthropic key or GitHub PAT rejected

**Symptoms.** `agentctl init` exits with `anthropic key rejected
(status 401)` or `github PAT rejected (401)`. `agentctl doctor`
shows `secrets.fresh FAIL`.

**Fix.** Generate a fresh token and re-prompt:

    agentctl init --reset-token anthropic
    agentctl init --reset-token github

If you need to skip the live probe (e.g. offline development), set
`AGENTCTL_SKIP_ANTHROPIC_VALIDATE=1` or
`AGENTCTL_SKIP_GITHUB_PAT_CHECK=1` before invoking `init` or
`doctor`. The check is logged as `skip` rather than `ok`.

## Service install fails (no systemd-user, no launchd)

**Symptoms.** `agentctl init` prints `service install failed
(...); falling back to foreground for this session`. `agentctl
doctor` shows `service.active warn`.

**Fix.** This is acceptable — agentctl runs the daemon in the
foreground for the duration of `init`. To run agentd manually
afterwards:

    agentctl init --foreground &        # background it from your shell

Or invoke the daemon binary directly:

    agentd

For a permanent install on Linux without systemd-user, you can wire
agentd into your preferred process supervisor (it expects
`AGENTCTL_HOME` to point at the user home if not running as the
target user).

## DB corruption

**Symptoms.** `agentctl doctor` shows
`db.integrity FAIL integrity_check=...`. `agentd` refuses to start
with `migrate:` or `integrity_check:` errors.

**Fix.**

1. Try `agentctl doctor --repair-db`. This runs sqlite `VACUUM`
   followed by `PRAGMA integrity_check`. If the second pass returns
   `ok`, you are done.
2. If `--repair-db` reports
   `DB corruption beyond repair; restore from backup.`, restore
   `~/.local/share/agentctl/agentd.db` from your latest backup
   (or, if you have none, delete the file and re-run
   `agentctl init` to start fresh — you will lose session
   history but configured MCPs and skills survive on disk).

## Reboot recovery

agentd is designed to survive reboots. On boot it reconciles the DB
against Docker (containers labelled `agentctl.session=<id>`) and
adopts what it finds. If a session looks "stuck" after a reboot:

1. `agentctl ls` — confirm what agentd thinks is running.
2. `agentctl logs --daemon -f` — tail the agentd journal for
   `recovery.*` events.
3. `agentctl doctor` — `agentd.health` should be `ok`. If it's
   `FAIL`, the daemon is not running; start it (`systemctl --user
   start agentd` on Linux, or `launchctl kickstart -k
   gui/$UID/com.agentctl.agentd` on macOS).
4. `agentctl restart <session>` — discards the stale container and
   recreates it from the currently pinned image. The session
   volume is preserved.

## Web UI won't load

**Symptoms.** Browser shows
`This site can't be reached` for `127.0.0.1:7777`, or the page
loads but the SPA reports an auth error.

**Fix.**

1. `agentctl doctor` — `agentd.health` must be `ok`.
2. `cat ~/.config/agentctl/web_token` — it must be a non-empty
   base64 string and have mode `0600`. If it's empty or
   wrong-perm, `agentctl init --reset-web-token`.
3. Use `agentctl ui` to open the URL — it embeds the token in the
   URL fragment so the SPA can authenticate.
4. If you've changed `agentd.web_addr`, ensure the bind address is
   reachable from the browser.

## Session won't start

**Symptoms.** `agentctl start` returns `create session: ...` with a
docker, image, or skill error.

**Fix.**

1. `agentctl doctor` — fix any FAIL rows first, especially
   `docker.reachable`, `image.present`, and `image.built`.
2. If the error mentions a skill, run `agentctl skill validate
   <name>` to see the manifest issue.
3. If the error mentions an MCP, run `agentctl mcp list` and
   verify the URL/transport/kind. The `mcp.registry` doctor check
   warns on unknown values but tolerates them at runtime.

## Where to look next

- Daemon log: `agentctl logs --daemon -f` (Linux: journalctl;
  macOS: `~/Library/Logs/agentctl/agentd.log`).
- Per-session log:
  `~/.local/share/agentctl/sessions/<id>/agentd.log`.
- Container stdout/stderr: `agentctl logs <session> --container`.
- Last error: `~/.local/state/agentctl/last-error.log`.
- Crash dumps:
  `~/.local/state/agentctl/crash-<timestamp>.log` (kept for the
  last 10 panics).

If you've gone through the above and are still stuck, open an
issue with the output of `agentctl doctor --json`, the relevant
log excerpt, and your platform / Docker version.
