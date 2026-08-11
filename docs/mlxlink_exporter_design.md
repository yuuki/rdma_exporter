# mlxlink_exporter Design

This document covers `mlxlink_exporter` only. The sysfs-based exporter is described in [design.md](design.md); the two share a repository and nothing else at runtime.

## 1. Background and Goals

`rdma_exporter` reads everything the kernel already knows through sysfs. The physical layer below that — bit error ratios, per-lane raw errors, FEC histograms, SerDes tuning and transceiver diagnostics — lives in the adapter firmware and is only reachable through NVIDIA's MFT tooling, in practice `mlxlink`.

Measurements on the target hardware:

| Invocation | Wall time | CPU time |
| ---------- | --------- | -------- |
| `mlxlink -d <dev> -m -c --json` (baseline) | 0.77–0.78 s | 0.69 s |
| `mlxlink -d <dev> -m -c --rx_fec_histogram --show_histogram --show_serdes_tx --json` | 0.83 s | – |
| Previous command plus `--show_eye` (network Eye) | 0.83 s | – |
| `mlxlink -d <dev> --port_type PCIE --show_eye --json` | 0.33 s | – |

An `strace` of the run shows the time going into firmware access, not into formatting or into the extra `-m` section. That single fact drives the whole design:

- **A scrape may not run `mlxlink`.** At 0.83 s per device, a host with eight adapters would need more than six seconds per scrape, and concurrent scrapes would multiply firmware access. Collection therefore runs in the background on its own schedule and `/metrics` serves a cache.
- **Network sections stay combined.** Adding the FEC histogram and SerDes TX sections to the baseline query increased the observed wall time by only about 0.05–0.06 s because firmware access dominates the cost. Network Eye is therefore another opt-in section of the same query. Root-PCIe Eye describes a different link and requires `--port_type PCIE`, so it uses an independent snapshot and runs only after all network work.
- **A separate binary.** `mlxlink` may need privileges that `rdma_exporter` must never hold, and firmware access can hang in ways sysfs reads do not. Keeping the two in separate processes keeps that blast radius contained; `rdma_exporter` is unchanged by this addition.

Goals: expose physical-layer health without ever letting a scrape wait on firmware; make every failure visible as a metric; never publish a number the tooling did not actually report.

## 2. Requirements

- **Platform**: Linux only. The binary builds on other platforms for development, but discovery reads `/sys/class/infiniband` and the collection target is a Linux host with MFT installed.
- **Dependencies**: `mlxlink` from NVIDIA MFT, verified against MFT 4.34.1 on one ConnectX-7 system. Devices are addressed by IB device name (`-d mlx5_0`), so `/dev/mst` nodes are not required.
- **Service interface**: `/metrics`, `/healthz` (liveness, always `200`), `/readyz` (`503` until one device has been collected). Default listen address `:9880`.
- **Configurability**: thirteen flags. The twelve that configure the service are mirrored by `MLXLINK_EXPORTER_*` environment variables; `--version` is CLI only. Both Eye flags default to false.
- **Non-goals**: container images (see §7), active device configuration, per-port collection on multi-port adapters (see §7), and any change to `rdma_exporter`'s metrics, flags or images.

## 3. High-Level Architecture

```
┌──────────────────┐
│ main (cmd)       │
│ - parse flags    │
│ - stat mlxlink   │
│ - wire deps      │
└───┬──────────┬───┘
    │          │
    │   ┌──────▼──────────────┐        ┌──────────────────┐
    │   │ Poller (goroutine)  │        │  mlxlink binary  │
    │   │  sweep loop, 1 at   │  exec  │ fixed network and│
    │   │  a time, per tick   ├───────►│ PCIe Eye queries │
    │   │                     │        │ --json           │
    │   │                     │        └──────────────────┘
    │   │  Discovery ─► Runner ─► Decoder                  
    │   └──────┬──────────────┘
    │          │ store (copy-on-write)
    │   ┌──────▼──────────────┐
    │   │ atomic.Pointer      │  independent immutable network
    │   │ snapshot stores     │  and root-PCIe Eye sets
    │   └──────┬──────────────┘
    │          │ load
┌───▼──────────▼──────┐      ┌─────────────────────┐
│ Collector           │◄─────┤ /metrics (promhttp) │
│ - cache reads only  │      └─────────────────────┘
│ - no exec, no sysfs │
└─────────────────────┘
```

Network measurements and root-PCIe Eye measurements pass through separate atomic snapshot sets. On either path a scrape never takes a lock, never blocks on I/O, and cannot observe a half-written device: each pointer is swapped to a fully built, immutable set. Network and PCIe Eye also have independent collectors and error counters. The PCIe collector and counter are registered only when `--show-pcie-eye` is enabled. A `prometheus.CounterVec` synchronizes internally (its `Collect` briefly takes a read lock), which is the only locking a scrape can encounter.

## 4. Data Flow

1. `Poller.Run` sweeps immediately at start-up, then once per `--poll-interval`.
2. Each sweep re-runs discovery, so hotplugged devices appear and removed ones fall out of the published set without a restart.
3. For every discovered target, in sequence: the poller executes the normal combined query, or the Eye-enabled combined query when `--show-eye` is set. `Decode` turns JSON into `PortData`, and the network snapshot for that device is updated. Only non-zero exits advance through the staged fallback described in §5.4.
4. After each device the whole set is republished, with devices this sweep has not reached yet carried over from the previous set. A scrape that lands mid-sweep therefore sees fresh data for the devices already visited and the previous data for the rest, never a gap.
5. Readiness is announced only after the successful snapshot has been published, so a reader that acts on `/readyz` can always scrape the data behind it.
6. If `--show-pcie-eye` is enabled, the poller next visits every discovered target again for the root-PCIe Eye query. This low-priority phase starts only after all network snapshots have been attempted, and shares the same goroutine, so process concurrency remains one.
7. A scrape loads the current network and optional PCIe Eye sets and turns them into metrics. Staleness is judged independently per device and snapshot kind at that moment.

## 5. Component Design

### 5.1 Discovery (`discovery.go`)
Walks `<sysfs-root>/class/infiniband`, accepting both real directories and the symlinks the real sysfs uses. For each device it reads the `device` symlink for the PCI address and treats the presence of `device/physfn` as proof of an SR-IOV virtual function, which is skipped: a VF shares the physical function's firmware and `mlxlink` addresses the PF. Devices named in `--exclude-devices` are skipped. The lowest numbered port is chosen numerically, not lexicographically, and the associated netdev is read from `ports/<n>/gid_attrs/ndevs` when present. A missing class directory means "this host has no RDMA devices", not an error.

### 5.2 Runner (`runner.go`)
Executes the binary directly, never through a shell, with `LC_ALL=C` so number formatting cannot drift with the host locale. Failures are classified into an `ErrorReason` (§6) that becomes a metric label.

The runner exposes four fixed argument vectors; the device name is the only substituted value:

- Normal network: `-d <device> -m -c --rx_fec_histogram --show_histogram --show_serdes_tx --json`.
- Network with Eye: the normal vector plus `--show_eye` before `--json`.
- Baseline fallback: `-d <device> -m -c --json`.
- Root-PCIe Eye: `-d <device> --port_type PCIE --show_eye --json`.

There are no query-type metric labels; `reason` remains the closed failure-cause set in §6.

Three properties are worth naming:

- **Bounded output.** stdout is capped at 4 MiB and stderr at 4 KiB. Passing the stdout cap kills the process and fails the run, and that decision is made from the buffer state rather than from the exit status: a binary that overproduces and then exits successfully must not have its truncated output accepted.
- **Bounded lifetime.** The child runs in its own process group and cancellation kills the group, because a grandchild that inherited stdout would otherwise hold the pipe open long past the timeout. `Cmd.WaitDelay` bounds the wait even then. The kill is skipped once the child has been reaped, since the pid could by then name an unrelated process group.
- **Shutdown is not a failure.** A cancelled parent context is returned as-is instead of being wrapped, so stopping the exporter never inflates the error counters.

### 5.3 Decoder (`decoder.go`)
`mlxlink --json` nests everything under `result.output.<section>`, with values in three shapes: plain strings, strings carrying a range suffix (`"61 [-10..80]"`), and per-lane objects (`{"values": ["0", "0", "0", "0"]}`). Analog per-lane readings arrive as one comma-joined string (`"265.504,265.504,248.416,248.416 [40..480]"`).

- **Base-field aliases are centralised.** MFT-specific spellings for the module, operational and physical-counter fields live in `fieldAliases`, as do section names. The FEC and SerDes parsers own their internal structural keys, lane headers and allowlisted parameter names because those sections require schema-level validation rather than scalar field lookup.
- **Development was fixture-gated.** The baseline, FEC/SerDes, network Eye and root-PCIe Eye captures under `testdata/mlxlink/` are real MFT 4.34.1 responses. The serial number in the Eye capture is replaced with `<redacted>`. The derived fixtures for missing fields, lane counts, faults and status errors are mechanical transformations of the baseline capture only.
- **Implicit lanes use zero-based positions.** Array and comma-joined fields without a lane list use their position in the reported list; Eye uses the explicit vendor lane numbers described below.
- **Units are normalised here**, not in the collector, and only where `mlxlink` uses a milli prefix: `Voltage [mV]` and `Bias Current [mA]` are divided by 1000 so the metrics carry volts and amperes. Temperature (Celsius) and optical power (dBm) are exported exactly as reported; no attempt is made to convert them into anything else.
- **Only an untrustworthy response fails**: malformed JSON, a non-zero `status.code`, or a missing `result.output`. The status is judged before the payload shape, so mlxlink's own message survives a payload this decoder cannot parse. A missing section or field merely leaves its values invalid, so one renamed key cannot cost a device every metric.
- **Missing is not zero.** `N/A`, an empty string and anything unparsable yield an invalid value, which the collector omits. Flag families are stricter: they accept only `0`/`1` and are taken all-or-nothing, because a partially decoded family would renumber its lanes relative to its neighbours. `DataPath state` compares exactly against `DPActivated`, so an unrecognised state reads as inactive and an absent one voids the family.
- **The FEC histogram is all-or-nothing.** The decoder requires the `Range`/`Occurrences` header, consecutive bins starting at zero, two values per bin, a non-negative `[N]` or `[low:high]` range, and a non-negative integer occurrence count. Any invalid bin omits the whole family, preserving the meaning of the disjoint, non-cumulative vendor ranges.
- **SerDes headers define the lane schema.** The decoder zips each lane's values with the unique parameter header, ignores parameters outside the fixed FIR and drive-amplitude allowlist, and omits a lane whose known values are not finite numbers. A duplicate header, invalid lane number or header/value length mismatch invalidates the whole SerDes section.
- **Eye lanes are explicit.** The decoder zips each measurement array to the reported `Lane` array and sorts the result by lane number. Missing required arrays, duplicate or negative lanes, and array-length mismatches invalidate the entire Eye family. A non-finite or unparsable score drops that lane across all fields. Network Eye requires initial/last FOM and upper/mid/lower grades; PCIe Eye requires only initial/last FOM. `FOM Mode` and unknown fields are ignored because their stable value sets are not established.

### 5.4 Poller (`poller.go`)
One goroutine, one sweep at a time, devices visited in sequence. Concurrency of one is structural rather than enforced by a lock, which also spreads the invocations across the interval instead of bursting them.

A failed device keeps its previous data and last-success timestamp; only `LastError` and `LastDuration` change. Stale data with a visible error beats no data, and the staleness horizon (§5.5) bounds how long that can go on.

Only a non-zero process exit triggers fallback. Without network Eye, the normal combined query falls back directly to baseline. With `--show-eye`, the Eye-enabled combined query first falls back to the normal combined query; only a second non-zero exit advances to baseline. A successful normal-combined fallback clears Eye but preserves FEC and SerDes. A successful baseline fallback clears Eye, FEC and SerDes. Each rejected query is counted as `exit_error` after the next attempt returns without shutdown cancellation, while a successful fallback clears `LastError`, so `mlxlink_collector_up` remains 1. `LastDuration` covers every invocation in the staged attempt. Timeout, permission, oversized-output and cancellation failures do not advance to another query; an invalid JSON response is likewise a decode failure rather than a fallback trigger.

Root-PCIe Eye collection starts only after the complete network phase and has no fallback. It updates a separate snapshot and error counter, so its failure never changes the network snapshot, readiness or `mlxlink_collector_up`. Previous PCIe Eye data and last-success time survive a failure; only its independent error and duration change.

If a tick fires while a sweep is running, the sweep that could not start is recorded as `reason="overlapping"`. Exactly one pending tick is consumed per sweep: Go tickers coalesce the ticks they missed, so no more than one can be pending however long a sweep runs, and taking one keeps the drain finite. The channel length cannot be used to size that drain — since Go 1.23 a ticker channel reports a length of 0 while a tick is pending.

The count is an approximation in both directions. Coalescing makes it undercount: a sweep that overran three intervals is still one increment. In the other direction, a tick that arrives in the instant between the sweep finishing and the drain reading the channel is consumed as if it had been waiting, which records an overlap that did not happen and slips that sweep by one interval. The false positive is confined to a narrow race and slips one sweep; the undercount is not rare — a persistently overrunning sweep undercounts on every interval, and missed increments are never made up later. The metric answers "is the interval too short", not "how many sweeps were lost".

### 5.5 Collector (`collector.go`)
Reads the snapshot and nothing else: no `exec`, no sysfs, no locks. All descriptors are static and built in the constructor, and registering each one as it is created keeps `Describe` from drifting out of sync with `Collect`.

- **Staleness**: a device whose last success is older than `--poll-interval` × 5 (150 s by default), or which has never succeeded, stops exporting measurement series while its self-monitoring series continue. The exporter keeps reporting that it cannot collect rather than falling silent, and Prometheus sees a gap in the measurements rather than a frozen value.
- **`mlxlink_collector_up`** is 1 when the most recent poll of that device succeeded and it has succeeded at least once. It describes the last poll, not the age of the data, so a device can report `up=1` while its measurements are suppressed as stale: the poller may have stopped, or a sequential sweep over many slow devices may simply be taking longer than the staleness horizon.
- **`mlxlink_collection_last_success_timestamp_seconds`** is omitted entirely for a device that has never succeeded, so the series never claims a successful collection in 1970.
- **`mlxlink_collection_duration_seconds`** covers the latest collection attempt, including every invocation in the fallback chain, rather than the duration of only the last process.
- **Network Eye uses the network identity.** `mlxlink_eye_fom{device,port,pci_addr,lane,stage}` exports `initial` and `last`; `mlxlink_eye_grade{device,port,pci_addr,lane,position}` exports `upper`, `mid` and `lower`.
- **PCIe Eye is independent.** `mlxlink_pcie_eye_fom{device,pci_addr,lane,stage}`, `mlxlink_pcie_eye_collector_up{device,pci_addr}`, `mlxlink_pcie_eye_collection_duration_seconds{device,pci_addr}`, `mlxlink_pcie_eye_collection_last_success_timestamp_seconds{device,pci_addr}` and the error family in §6 deliberately have no network `port`. PCIe Eye uses the same `poll interval × 5` staleness horizon and keeps exporting self-monitoring after measurements become stale.
- **Counter resets have separate operations.** `mlxlink --clear_counters` clears the physical counters, while `mlxlink --clear_histogram` clears FEC histogram occurrences. Adapter, hardware or firmware resets may also return the histogram to zero. The exporter invokes neither destructive operation; they are documented only so a return to zero is not misread as an absence of errors.
- Invalid values and empty lane lists are skipped per series, and the two `_info` families are skipped when every label they carry is empty.

## 6. Error Taxonomy

`mlxlink_collection_errors_total{device,port,pci_addr,reason}` uses a closed set of reasons:

| `reason` | Raised by | Meaning |
| -------- | --------- | ------- |
| `timeout` | runner | `--command-timeout` expired, or the output pipe stayed open past the wait delay |
| `command_not_found` | runner | The binary at `--mlxlink-path` does not exist (start-up also fails fast on this) |
| `permission_denied` | runner | The process may not execute the binary (`EACCES`/`EPERM`) |
| `exit_error` | runner | `mlxlink` ran and exited non-zero; the truncated stderr is logged at debug level |
| `output_too_large` | runner | stdout passed 4 MiB and the process group was killed |
| `invalid_json` | poller | `Decode` rejected the response: malformed JSON, non-zero `status.code`, or no `result.output` |
| `overlapping` | poller | A tick fired while the previous sweep was still running |
| `unknown` | runner | A failure none of the above describes |

Note that a `mlxlink` that reports insufficient privileges by exiting non-zero appears as `exit_error`, not `permission_denied`: the latter is reserved for failures to execute the binary at all. Both are covered by the verification procedure in [deployment.md](deployment.md).

When PCIe Eye is enabled, `mlxlink_pcie_eye_collection_errors_total{device,pci_addr,reason}` uses the same closed reason set without a `port` label. PCIe errors are not copied into the network counter.

## 7. Known Limitations

- **Multi-port adapters**: `mlxlink` is invoked as `-d <device>` without `-p`, so only the lowest port of a device is collected. A device with more ports logs a warning once. Supporting `-p` would multiply the per-sweep cost by the port count, so it is deferred until someone needs it.
- **Grandchild processes**: cancellation kills the process group, but `os/exec` stops watching the context once the direct child exits. A grandchild that outlives its parent is therefore not signalled; the wait delay still bounds the caller by closing the pipes. `mlxlink` is not known to fork such children — this is a bound on the failure mode, not an observed one.
- **Overlap accounting is approximate both ways**: ticker coalescing makes one overrun sweep a single `overlapping` increment regardless of how many intervals it spanned, and a tick landing exactly during the drain is counted as an overlap that never happened (§5.4). The metric detects "the interval is too short", not "how many sweeps were lost".
- **Hosts without RDMA devices** never become ready: `/readyz` stays `503` for the lifetime of the process. This is deliberate — there is nothing to serve — but it means readiness cannot be used as a generic liveness signal. `/healthz` is the one for that.
- **MFT version drift**: key spellings differ between MFT releases. For base fields and section names, add the new spelling to `fieldAliases` alongside the old one. For FEC, SerDes or Eye structural changes, update the dedicated parser's schema rules or parameter allowlist. In both cases add a real fixture rather than branching on versions.
- **Eye qualification is narrow**: both Eye flags default to false. Network combined Eye took 0.83 s and root-PCIe Eye took 0.33 s in single measurements on MFT 4.34.1 with one ConnectX-7. No latency guarantee or output compatibility is claimed for other MFT releases, adapters, cables, line rates or link states. PCIe collection addresses only the root link selected by the default depth/index/node.
- **No container image**: `mlxlink` belongs to MFT and talks to the adapter firmware. Bundling MFT into the image would mean shipping vendor tooling with kernel-adjacent access, so `dockers` in `.goreleaser.yaml` stays limited to `rdma_exporter` and this exporter runs on the host.

## 8. Security Considerations

- **Root is not assumed.** The shipped unit runs as a dedicated unprivileged user with an empty capability bounding set. What a given host actually needs depends on its MFT packaging and device ownership, so the required privilege is determined by observation (deployment.md §"Verify the privileges") and granted in the smallest increment that works.
- **The exporter only reads.** It never passes user input to `mlxlink`: the argument vector is fixed and the device name comes from a sysfs directory listing. The binary is executed directly, never through a shell.
- **Output stays out of the metrics.** stderr goes to the debug log only, never to a label; the module serial number is exported as a `mlxlink_module_info` label by design, but no free-form tool output ever becomes one. Metric labels stay bounded to values the decoder recognises.
- **Failure is visible, not fatal.** A device that cannot be collected raises counters and keeps serving its last successful values, dropping its measurement series only once the staleness horizon has passed. It never causes the process to exit or a scrape to fail.

## 9. Testing Strategy

- **Fixture-driven decoding.** The baseline golden test pins every field of the real MFT 4.34.1 baseline capture against hand-checked expectations, including unit conversions. Separate real captures pin FEC/SerDes, four-lane network Eye, and sixteen-lane root-PCIe Eye output; the Eye serial number is redacted. Table-driven tests cover one, four, eight and sixteen lanes, malformed arrays, duplicate lanes, non-finite values and absent sections.
- **Deterministic concurrency tests.** The poller is tested through a fake clock, fake ticker, fake discovery and fake runner. Sweep behaviour is driven synchronously; the `Run` loop is synchronised on events the poller genuinely produces (ticker creation, runner calls, a log record written after accounting completes) rather than on sleeps.
- **Process-level tests.** The runner is exercised against generated shell scripts covering all four argument vectors, locale, timeout, exit status with an oversized stderr, a missing binary, a non-executable binary, an unbounded flood of stdout, and a lingering grandchild.
- **Metric-level tests.** Both collectors are compared against raw exposition text, linted with `promlint`, and driven end to end from captured fixtures through the poller. Fallback tests cover both network stages, cleared optional families, an unrecovered previous snapshot and failure accounting. PCIe tests cover network-first ordering, failure isolation, retained data and staleness.
- Everything runs under `go test -race`.
- **Benchmark**: `BenchmarkMlxlinkCollectorCollect` collects 8 devices × 8 lanes with every field populated in under 1 ms per scrape in repeated local measurements (Apple M4, ~1,216 series, ~24.1k allocations per operation). The target was 50 ms, so the cache read is not a scrape-time concern.

## 10. Future Work

- Add observed MFT spellings to `fieldAliases` for base fields and section names, or to the dedicated FEC, SerDes and Eye parsers for structural keys and parameter names, with a real fixture for each new output shape.
- Optional per-port collection (`-p`) for multi-port adapters, if the extra invocations are acceptable.
- Export `Time Since Last Clear` so that counter resets can be correlated instead of inferred.
- Qualify Eye output, execution time and link safety across additional MFT versions, adapters, cables, line rates and link states before enabling either Eye collector by default.
- Add explicit PCIe depth/index/node selection or enumeration if root-link-only collection proves insufficient.
