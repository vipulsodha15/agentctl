# Contributing to agentctl

Thanks for your interest. agentctl is open-source under
[Apache 2.0](LICENSE); contributions of any size are welcome.

## Quick checklist

- Open an issue first for anything non-trivial — a new feature, a
  protocol change, or a refactor that touches more than a couple of
  packages. A 5-minute design conversation up front saves an afternoon
  of rework.
- Small fixes (typos, obvious bugs, docs) can skip straight to a PR.
- All code is reviewed before merge. Be patient — the maintainers do
  this in their evenings.
- By submitting a contribution you agree it is licensed under Apache 2.0
  per the terms in the [LICENSE](LICENSE) file (see "Submission of
  Contributions"). No separate CLA.

## Setting up

Requires Docker (Desktop or Engine), Go 1.24+, and Node 20+.

    git clone https://github.com/agentctl/agentctl.git
    cd agentctl
    bash installer/install.sh    # builds the session base image + binary
    go test ./...                # Go test suite
    cd web && npm ci             # SPA deps

To work on the Web UI:

    cd web && npm run dev        # Vite dev server with HMR
    # Or, to rebuild the embedded bundle that agentd serves:
    npm run build

## Running the suite

| What | Command |
|---|---|
| Go unit + race tests | `go test -race -count=1 ./...` |
| Go vet + formatting | `go vet ./... && gofmt -l .` (must be empty) |
| SPA typecheck | `cd web && npm run typecheck` |
| SPA build | `cd web && npm run build` |
| `go mod` tidiness | `go mod tidy` (must not change `go.mod` / `go.sum`) |

CI runs all of the above on every PR — see
[`.github/workflows/ci.yml`](.github/workflows/ci.yml). If CI is red and
you can't reproduce locally, ping the maintainers on the PR.

### Coverage

The Go job emits `coverage.out` and uploads it to
[Codecov](https://codecov.io/gh/vipulsodha15/agentctl). Public-repo uploads
work tokenless; if you fork into an org that requires a token, set the
`CODECOV_TOKEN` repo secret.

## Coding style

- Go: `gofmt`-clean, idiomatic; prefer the stdlib. New dependencies need
  a one-line justification in the PR description.
- TypeScript: `tsc --noEmit` clean. Follow the conventions already in
  `web/src/`.
- Architecture decisions live in `architecture/decisions/` as ADRs.
  Significant changes should add or update an ADR.
- Commit messages: short imperative subject line, optional body
  explaining the *why*. One logical change per commit.

## What to work on

- See [issues labelled `good first issue`](https://github.com/agentctl/agentctl/labels/good%20first%20issue).
- The `provider` label tracks new agent-runtime integrations (Cursor,
  opencode, Aider, …).
- If you have an idea that isn't already an issue, open one to discuss
  before writing code.

## Reporting bugs

See [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) first. If you still need
to file an issue, include:

- `agentctl version`
- `agentctl doctor --json` output (scrub anything sensitive)
- OS / Docker version
- A minimal reproduction

## Security issues

Please **do not** open a public issue for security vulnerabilities.
See [`SECURITY.md`](SECURITY.md) for the private reporting process.
