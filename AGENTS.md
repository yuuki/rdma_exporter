# Repository Guidelines

Local cache prefixes: `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache`.

## Layout
- `main.go`: CLI; wires config, `log/slog`, and HTTP `/metrics` + `/healthz`.
- Packages: `internal/collector`, `internal/config`, `internal/rdma`, `internal/server` (tests alongside).
- Architecture/roadmap: `docs/`. RDMA fixtures: `internal/rdma/testdata/<scenario>/` (extend when modeling new hardware or kernel behaviour).

## Commands
- `make build` → `./rdma_exporter`
- `make test` or `go test ./...`
- `make lint` (`go vet ./...`)
- `make fmt`

## Conventions
- `slog` keys: `device`, `port`, `duration`.
- Tests: table-driven; `prometheus/testutil` for metric output; fixtures via relative paths under `internal/rdma/testdata/`; names `Test<Component>_<Scenario>`.
- Commits: `type(scope): subject` (imperative). Types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `init`, `rearrange`, `update`. Body sections: `Motivation`, `Changes`, `Tests` (complete sentences, no abbreviations). Ticket IDs in the subject when applicable.
- PRs: summary, test evidence, issue links. New metrics: include a sample `/metrics` snippet.

## Ops
- Default scrape timeout is 5s (`--scrape-timeout`).
- Run unprivileged with read-only `/sys/class/infiniband`.
- Internet-facing: expose only `/metrics` and `/healthz`; terminate TLS upstream.

## Release
1. Bump `version` in `main.go`; refresh release docs.
2. Annotated tag: `git tag -a vX.Y.Z -m "Release X.Y.Z"`.
3. `git push origin main --tags` (GitHub Actions `release` + GoReleaser: Linux amd64/arm64).
