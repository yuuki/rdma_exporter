# AGENTS.md

This file holds only what the code does not show. The design and its
rationale are in [docs/design.md](docs/design.md).

## Rules the code does not show

- Read-only: never write sysfs, never issue `STAT_SET` / `rdma statistic set`,
  never bind QPs or enable auto mode; enabling counters is the operator's job.
- Pull-only: a scrape reads the kernel afresh. Add no background work and no
  cache across scrapes (only "unsupported" verdicts are cached).
- Providers must honour `ctx`: at the scrape timeout the server answers 504,
  but the gathering goroutine runs until the providers return.
- Emit series through `constMetric` (`internal/collector`), which makes label
  values valid UTF-8; never pass external strings to
  `prometheus.MustNewConstMetric` directly.
- Cardinality: never export LQPN, PID, `counter_id`, GIDs or IPs as labels;
  tool or kernel strings go to logs, never into new labels.
- `//nolint` only as `//nolint:<linter> // <reason>`. English only.

## Compatibility contracts

Call a change out in the PR and in `docs/releases/`.

- Metric names, types and label sets (adding a label changes series
  identity): `internal/collector/collector.go`, `internal/collector/netdev.go`;
  `dashboards/` queries them.
- CLI flags and `RDMA_EXPORTER_*` variables: `internal/config`; removed flags
  keep failing with their rename message.
- HTTP endpoints: `internal/config/config.go`, `internal/server/server.go`.
- NETLINK_RDMA command and attribute numbers (`internal/rdmanl/attr.go`) are
  kernel ABI: never renumber.
- Sysfs paths read: `internal/rdma/provider.go`.
- Shipped units (`deploy/systemd/`); image name and archive layout
  (`.goreleaser.yaml`).

## Before opening a pull request

- `make check`.
- `internal/rdmanl` client code: also `make test-integration` (root).
- A parser (sysfs, netlink, ethtool names, label building): also
  `make test-fuzz FUZZTIME=1m`.
- Metrics: README "Metrics", `docs/design.md`, the dashboards
  (`make grafana-com-export`), and a sample `/metrics` snippet in the PR.

## Git conventions

Branches: `<type>/<issue>-<summary>`, or `<type>/<summary>` without an issue
(`fix/166-connect-errors`, `feat/qp-counters`). `<type>` is a commit type
below; `<summary>` is 2–5 lowercase ASCII words joined by `-`. Branches named
by tools keep their names (`agent/*`, `claude/*`, `copilot/*`,
`dependabot/*`). Never push to the default branch.

Commits follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

    <type>(<scope>)!: <subject>

    <body>

    <footer>

- `type`: `feat` (new user-visible behaviour), `fix` (bug fix), `perf`,
  `refactor` (no behaviour change), `test`, `docs`, `build` (build system,
  dependencies, packaging), `ci` (workflows), `chore` (anything else, e.g.
  `chore(release): prepare v1.2.3`), `revert`.
- `scope`: optional; the package or area changed: `collector`, `rdma`,
  `rdmanl`, `netdev`, `config`, `server`, `dashboards`, `deploy`.
- `!` and a `BREAKING CHANGE: <what changed and how to migrate>` footer for
  any change to a compatibility contract above.
- `subject`: imperative, lower case (except identifiers), no final period,
  at most 72 characters including the prefix.
- `body`: why the change is needed and what it affects, wrapped at 72
  columns; required unless the subject says everything.
- `footer`: `Fixes #N` / `Refs #N`, `BREAKING CHANGE:`, trailers such as
  `Co-Authored-By:`.
- One logical change per commit; regenerated files go in the commit that
  changes their source. Pull request titles use the same format (they become
  the squash commit).

## Forbidden

- Disabling, skipping, weakening or deleting tests, fuzz properties or lint
  rules to get green (no blanket excludes, `|| true`, `continue-on-error`).
- Editing `dashboards/*.grafana.com.json` by hand (`make grafana-com-export`).
- Adding dependencies, test-only ones included, without a justification.
- Changing a compatibility contract without calling it out.
- Loosening workflow permissions, unpinning actions, or adding build caches
  to the release workflow.
- Committing secrets, rewriting history, force-pushing, tagging or publishing
  releases.

## Needs human review

Even with green CI: `.github/`, `.goreleaser.yaml`, Dockerfiles; permissions
and secrets; netlink and sysfs parsing (untrusted input); anything near
privileged operations or the read-only rule; the compatibility contracts;
new dependencies; large refactors.

## Environment notes

- In shared or sandboxed environments, prefix Go commands with
  `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache` (both gitignored).
- With `GH_TOKEN` set but the GitHub API unreachable, run
  `env -u GH_TOKEN make check`.
