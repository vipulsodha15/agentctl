# ADR 0014 — Local image build and bind-mounted skills

- **Status:** Accepted.
- **Date:** 2026-05-10.
- **Deciders:** Product owner.
- **Supersedes (in part):** 0008 — image identity is now local-build, not registry-pull. The "explicit, never automatic" update principle from 0008 is retained.

## Context

The earlier design (ADR 0008, container-and-image.md §1) treated the
session base image as a **publisher artifact**:

- A team CI pipeline ran `docker build`, `cosign sign`, `docker push`
  against an OCI registry.
- Every developer's `agentctl init` ran `docker pull` against that
  registry, verified the cosign signature, and pinned the digest.
- Skills shipped as a `COPY skills/ /skills/` layer inside the image.

Two things were unsatisfying about this:

1. **Operational weight of being a publisher.** Maintaining a Docker
   registry, cosign keys/identity, and a release pipeline for a tool
   whose user count is "a development team" is over-engineered. It
   forces every fork or internal team adopting agentctl to set up the
   same publisher infrastructure.
2. **Skill iteration coupled to image rebuilds.** Adding or editing a
   skill required rebuilding the image, pushing it, and every developer
   running `agentctl update`. For per-developer custom skills this was
   completely impractical (the previous attempt to address this was
   the "skills overlay" sketch — itself a workaround for skills being
   baked at the wrong layer).

## Decision

Two tightly-coupled changes:

### 1. The base image is built locally on the developer's machine.

- `install.sh` lays down a complete Docker build context at
  `~/.local/share/agentctl/image/` containing the `Dockerfile`, the
  shim source (compiled inside the build via a multi-stage step), the
  entrypoint, and the runtime config templates. Same script that
  installs the binary lays this down — single signature-verified
  payload.
- `agentctl init` runs `docker build -t agentctl/session-base:local
  <build-context>` after the Docker check and before the system-service
  install. Build progress streams to the terminal; on completion the
  resulting image ID (a content-addressable sha256 over the image
  config) is pinned to `config.toml` `[image].pinned_id`.
- `agentctl update` re-runs `docker build` against the current build
  context and repins the image ID. The previous ID is retained as
  `[image].previous_id` for one-command rollback (re-tag, no rebuild
  needed since layers are cached).
- There is no OCI registry reference. There is no cosign verification of
  the image. Trust comes from `install.sh` having signature-verified the
  bundle that contains the Dockerfile and shim source. The locally-built
  image is a build artifact of inputs the developer already trusts.

### 2. Skills are bind-mounted into the container, not baked into the image.

- The image's `/skills/` directory is **empty** at build time. It exists
  only as a mount point.
- `install.sh` lays down the project's curated baseline skills at
  `~/.local/share/agentctl/builtin-skills/<name>/`. These are
  treated as immutable per-install (replaced atomically on upgrade by
  re-running `install.sh`).
- `agentd` manages developer custom skills under
  `~/.local/share/agentctl/custom-skills/<name>/`, mutated through the
  `agentctl skill ...` CLI surface.
- At each session start, `agentd`:
  1. Composes built-in + custom into a fresh per-session snapshot at
     `~/.local/share/agentctl/sessions/<id>/skills/` (custom wins on
     name collision; collisions emit a `skill.collision` event).
  2. Bind-mounts that snapshot into the container at `/skills/` read-only.
  3. Records the sha256 of the snapshot tree on the session row
     (`skills_snapshot_hash`) alongside `image_id` for reproducibility.
- The runtime auto-discovers `/skills/` as before — the runtime itself
  doesn't know whether the contents are baked or mounted.
- Adding/removing/editing a custom skill **does not** trigger an image
  rebuild. It only changes what the next session start composes.
  Live-reload mid-session is deferred to v2; for v1, "restart the
  session" is the path.

## Consequences

### Wins

- **No registry, no publisher infrastructure.** A team forking agentctl
  ships a tarball; their developers run install.sh + init. No Docker
  Hub account, no cosign, no GHCR.
- **Skills decouple from image releases.** Skill changes don't rebuild
  the image. Developers iterate on custom skills in seconds. Built-in
  skills update with `install.sh` re-run + a fresh init/update is no
  longer required for skill-only changes.
- **Per-developer customization is first-class.** `agentctl skill add`
  drops a skill into the custom-skills dir; the next session has it.
  No fork of the image, no team-rebuild flow.
- **Reproducibility per session is preserved.** Session row records
  both `image_id` and `skills_snapshot_hash`. A failing session can be
  reconstructed from those two pins plus the volume.
- **Smaller image to rebuild.** No "skills layer" means the image is
  pure runtime + tooling; a `docker build` after a skill change is a
  no-op (cache hit on every layer).

### Losses

- **First `agentctl init` is slow.** A from-scratch Docker build
  (debian-slim base + apt packages + Node + Python + npm install of
  the agent runtime + Go compile of the shim) takes 3–10 minutes
  depending on network. install.sh remains fast; init is the slow
  step. We document this and stream Docker's build output so the
  developer sees progress.
- **No cryptographic signature on the image itself.** The image is
  a local artifact; we don't sign local builds. Trust derives from
  install.sh's verification of the inputs. A team that wants
  signed images can build their own image once and distribute via a
  team-internal registry — that path is documented but not the v1
  default.
- **Apt/npm/Go dependency drift.** Two developers running `agentctl
  init` on different days may resolve different transitive package
  versions. The Dockerfile pins the agent runtime version (npm
  package) and the OS base tag, but apt/npm don't lock by default.
  Acceptable for v1; reproducible-build hardening is a v2 concern.
- **Skills changes need a session restart in v1.** Live-reload of the
  bind-mount mid-session would require the runtime to rescan and
  re-emit its skill list on a control-channel signal. Deferred to v2;
  in v1 the developer runs `agentctl restart <session>` (preserves the
  volume) to pick up new skills.

### Contract changes

- `config.toml` `[image]` no longer has `ref`, `pinned_digest`,
  `cosign_identity`. New fields: `pinned_id`, `previous_id`,
  `build_context_path`.
- `sessions` table column rename: `image_digest` → `image_id`. New
  columns: `skills_snapshot_hash`, `skills_snapshot_path`.
- New CLI: `agentctl skill {list,new,add,edit,remove,validate,show,export}`.
- `agentctl init` adds a "Building base image" phase between Docker
  check and token prompts. Build is non-interactive; failure aborts
  init with the captured build log.
- `agentctl update` is now a `docker build`, not a `docker pull`.
- `agentctl doctor` checks change: `image.signed` → `image.built`;
  add `skills.builtin` and `skills.custom`.
- `cosign verify` step in `agentctl init` and `update` is removed.

## Alternatives considered

- **Keep registry-pull for the base image, add a custom-skills overlay
  bind-mount** (the "v1.1 overlay" sketched in earlier design messages).
  Rejected because it leaves the publisher infrastructure problem in
  place and creates two parallel "where do skills come from" stories
  in the system. With local build there is one story: skills are mounted.
- **Bake built-in skills, mount custom skills** (asymmetric layering).
  Rejected because the asymmetry has no benefit once the image is
  locally built — the rebuild cost for a built-in skill change is the
  same as for any other; we may as well treat both uniformly via the
  mount.
- **Skip image build entirely, use a published `debian:bookworm-slim`
  + setup script at session start.** Rejected because it pushes
  apt/npm install latency from "once per init" to "once per session
  start," wrecking the cold-start budget (R2: ≤5s p50).

## References

- `requirements.md` R1 (init flow), R3 (session environment), R9 (skills).
- `architecture/container-and-image.md` (Dockerfile, mounts, build context).
- `architecture/install-and-update.md` (init phases, update flow).
- `architecture/data-model.md` ([image] config, sessions schema, on-disk layout).
- ADR 0008 (image and skill update path) — superseded for the
  mechanism (docker pull → docker build), retained for the principle
  (explicit, never automatic).
