# Repository Guidelines

## Project Structure & Module Organization
- `main.go` is the CLI entry point; it wires configuration, structured logging, and the HTTP server that exposes `/metrics` and `/healthz`.
- `internal/collector`, `internal/config`, `internal/rdma`, and `internal/server` separate exporter logic by concern, each with co-located unit tests under the same directory.
- Architectural and roadmap notes live in `docs/`. Synthetic RDMA fixtures reside under `internal/rdma/testdata/<scenario>/`; extend or refresh them whenever you model new hardware or kernel behaviour.

## Build, Test, and Development Commands
- `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache make build` compiles the exporter into `bin/rdma_exporter`, suitable for local runs or packaging.
- `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache make test` or `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache go test ./...` executes every unit test plus fixture-driven checks to catch regressions early.
- `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache make lint` runs `go vet ./...` to surface static-analysis warnings before code review.
- `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache make fmt` applies `gofmt` across all Go sources so diffs stay minimal and consistent.

## Coding Style & Naming Conventions
- Always rely on `gofmt`; do not hand-tune indentation or spacing.
- Prefer lowerCamelCase for unexported identifiers and PascalCase for exported APIs; keep packages focused and cohesive.
- When using `log/slog`, attach contextual keys such as `device`, `port`, and `duration` to aid observability.
- Limit comments to intent, invariants, or non-obvious decisions—avoid restating what the code already communicates.

## Testing Guidelines
- Use table-driven tests with the standard `testing` package; leverage `prometheus/testutil` to assert metric output.
- Reference fixtures under `internal/rdma/testdata/` via relative paths so tests remain hermetic.
- Name test functions `Test<Component>_<Scenario>` to make failing cases self-explanatory.
- Run `GOCACHE=$(pwd)/.gocache GOMODCACHE=$(pwd)/.gomodcache go test ./...` before every pull request to keep the suite green and flake-free.

## Commit & Pull Request Guidelines
- Write commits as `type(scope): subject`, imperative; detail motivation and behavioural change in the body.
- Accepted types include `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `init`, `rearrange`, and `update`; choose a scope that pinpoints the touched package or feature.
- Pull requests should include a concise summary, test evidence (logs, screenshots, or metric samples), and links to relevant issues. When introducing new metrics, paste a sample `/metrics` snippet for reviewers.

## Security & Operational Tips
- The default scrape timeout is five seconds; adjust with `--scrape-timeout` for slower fabrics.
- Run the exporter as an unprivileged user with read-only access to `/sys/class/infiniband`; never grant write permissions.
- In internet-facing deployments, expose only `/metrics` and `/healthz` and terminate TLS upstream (sidecar or ingress) to minimize attack surface.
