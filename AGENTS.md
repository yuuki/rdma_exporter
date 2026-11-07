# AGENTS.md

## Rules
- Read-only: never write sysfs, issue `STAT_SET` / `rdma statistic set`, bind QPs or enable auto mode; enabling counters is the operator's job.
- Pull-only: every scrape reads the kernel afresh; no background work and no cache across scrapes except the "unsupported" verdicts.
- Emit series through `constMetric` (`internal/collector`), never `prometheus.MustNewConstMetric` with sysfs, netlink or ethtool strings.

## Compatibility contracts
- Metric names, types and label sets: `internal/collector/collector.go`, `internal/collector/netdev.go`, queried by `dashboards/`.
- CLI flags and `RDMA_EXPORTER_*` variables (`internal/config`; removed flags keep failing with their rename message).
- HTTP endpoints (`internal/config/config.go`, `internal/server/server.go`); shipped units (`deploy/systemd/`); image name, archive layout (`.goreleaser.yaml`).
- NETLINK_RDMA command and attribute numbers (`internal/rdmanl/attr.go`) are kernel ABI: never renumber; sysfs paths read (`internal/rdma/provider.go`).

## Before opening a pull request
- `make check`; also `make test-integration` (root) for `internal/rdmanl` client code, and `make test-fuzz FUZZTIME=1m` for a parser or label building.

## Git conventions
- Branches: `<type>/<issue>-<summary>` (`<type>/<summary>` without an issue); tools' `agent/*`, `claude/*`, `copilot/*`, `dependabot/*` keep their names.
- Commits and PR titles: Conventional Commits 1.0.0, types `feat` `fix` `perf` `refactor` `test` `docs` `build` `ci` `chore` `revert`; scopes: `collector`, `rdma`, `rdmanl`, `netdev`, `config`, `server`, `dashboards`, `deploy`.
