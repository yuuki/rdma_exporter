# Security policy

rdma_exporter runs on RDMA hosts, usually as a long-lived service, and turns
what the kernel reports into Prometheus metrics. It reads sysfs
(`/sys/class/infiniband`, `/sys/bus/pci`, `/sys/class/net`) and queries the
kernel over NETLINK_RDMA and ethtool, all read-only: it never writes sysfs,
never enables counters (`STAT_SET`), never binds QPs and needs no
`CAP_NET_ADMIN` (see [Security considerations](docs/design.md#8-security-considerations)).
Its HTTP endpoints have no authentication or TLS of their own; they are meant
to be reached by Prometheus only, or to be put behind a proxy that adds them.

In scope are, for example: data from sysfs, netlink or ethtool that crashes
the exporter, fails every scrape or exhausts its memory (netdev names, for
one, are chosen by whoever holds `CAP_NET_ADMIN` in a network namespace);
anything that makes the exporter write to the kernel or gain privileges;
issues in the HTTP server, the container images or the release artifacts.
Exposing `/metrics` to untrusted networks is a deployment choice, not a
vulnerability.

## Supported versions

Security fixes are made on the `main` branch and released in the next
version. Only the latest release is supported.

## Reporting a vulnerability

Please do **not** open a public issue, pull request or discussion.

Report the vulnerability privately with GitHub's
[private vulnerability reporting](https://github.com/yuuki/rdma_exporter/security/advisories/new)
(the "Report a vulnerability" button of the Security tab). If that is not
possible, contact the author privately via the contact information on
https://github.com/yuuki.

Please include the rdma_exporter version (`rdma_exporter --version`), the
flags and environment it runs with, the kernel version and RDMA driver, what
an attacker controls (a process in a container, a network namespace, the
network in front of `/metrics`, ...), and the impact. You should receive an
answer within a few weeks; rdma_exporter is maintained on a best-effort basis.
