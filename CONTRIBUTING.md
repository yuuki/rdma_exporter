# Contributing

Thanks for helping improve rdma_exporter. Bug reports, hardware quirks and
pull requests are welcome.

- Open an issue first for larger changes (new collectors, metric renames,
  new flags) so the design can be agreed on before you write the code;
  `docs/design.md` describes the architecture and its constraints.
- Security issues go through [SECURITY.md](SECURITY.md), not public issues.
- Run `make check` before opening a pull request. Changes to the netlink
  client also need `make test-integration` (root), and parser changes a fuzz
  run (`make test-fuzz`). The `Makefile` lists every target.
- Add or update tests with every behaviour change; sysfs layouts go into
  fixture trees under `internal/rdma/testdata/sysfs/`. No RDMA hardware is
  needed to run the tests.
- New or changed metrics: update README "Metrics" and paste a sample
  `/metrics` snippet into the pull request.
- Commits, branches and pull request titles follow the Git conventions in
  [AGENTS.md](AGENTS.md).
- The exporter stays read-only: it must never write sysfs, enable counters or
  bind QPs.
