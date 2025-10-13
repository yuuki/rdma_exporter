# Repository Guidelines

## Project Structure & Module Organization
- `main.go`: CLI entry point that wires configuration, logging, and the HTTP server.
- `internal/`: exporter implementation broken into `collector`, `config`, `rdma`, and `server` packages. Each has matching unit tests under the same directory.
- `docs/`: design documentation and future architectural notes.
- `internal/rdma/testdata/`: synthetic sysfs trees used for tests; update or add fixtures when modelling new hardware.

## Build, Test, and Development Commands
- `make build`: compiles the binary to `bin/rdma_exporter`.
- `make test` or `go test ./...`: runs all unit tests, including fixtures-based checks.
- `make lint`: executes `go vet` across modules; run before submitting changes.
- `make fmt`: applies `gofmt` to every Go file; required prior to commits.

## Coding Style & Naming Conventions
- Go formatting must follow `gofmt`; avoid manual tweaks.
- Use Go module layout with package-local types/functions following lowerCamelCase names; exported items use PascalCase.
- Logging relies on `log/slog`; include structured keys (`device`, `port`, `duration`) where relevant.
- Keep comments concise and only where additional context is needed.

## Testing Guidelines
- Prefer table-driven tests and the standard library `testing` package; leverage `prometheus/testutil` for metric assertions.
- Store representative sysfs fixtures under `internal/rdma/testdata/<scenario>/` and reference them via relative paths.
- Test names should follow `Test<Component>_<Scenario>` to clarify intent.
- Run `go test ./...` before opening a pull request; target zero flakes.

## Commit & Pull Request Guidelines
- Commit messages follow `{type}({scope}): {subject}` (see `git log` for examples such as `feat(exporter): ...` or `docs(design): ...`). Subject must be imperative and ≤60 characters.
- Body should state motivation and behaviour change; include `TESTING:` footer summarizing validation steps.
- Pull requests should include a summary, testing evidence, and links to relevant issues. Attach screenshots or log excerpts when exposing new metrics or endpoints.

### Git Commit Style Guide
- Keep commit lines under 80 characters. Reserve the subject for ≤60 characters, written in imperative present tense, starting with a lowercase letter, and without trailing punctuation.
- Structure messages as `type(scope): subject` followed by a blank line, an optional body, another blank line, and an optional footer. Describe motivation and contrast to previous behaviour in the body using imperative, present-tense sentences.
- Types are limited to `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `init`, `rearrange`, and `update`. Choose a scope that pinpoints the component or package touched.
- Use the footer to note `TESTING:` steps, declare `BREAKING CHANGE:`, and reference tracking IDs such as `closes #123` or GitHub issues.

## Security & Configuration Tips
- Default scrape timeout is 5s; adjust via `--scrape-timeout` for slow hardware.
- Run the exporter as an unprivileged user with read access to `/sys/class/infiniband`; avoid granting write permissions.
- When packaging, expose only `/metrics` and `/healthz`; terminate TLS upstream (sidecar or ingress) if internet-facing.
