Prefix Go with `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache`.

Commands: `make build`, `make test`, `make lint`, `make fmt`. CI also runs `go test -race ./...`.

- slog keys: `device`, `port`, `duration`.
- Tests: table-driven; `prometheus/testutil` for metric text; sysfs fixtures under `internal/rdma/testdata/sysfs/<scenario>/`.
- Commits: headline `type(scope): subject` (imperative); body must state the motivation (why) in complete sentences. Extra types: `init`, `rearrange`, `update`.
- New metrics: include a sample `/metrics` snippet in the PR.
- Never write sysfs, never `STAT_SET` / `rdma statistic set`, never bind QPs or enable auto mode.

Architecture: `docs/design.md`.
