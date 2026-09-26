<p align="center">
  <img src="assets/wordmark.svg" alt="siltide" width="560">
</p>

<p align="center">
  <a href="https://github.com/moezdil/siltide/actions/workflows/ci.yml"><img src="https://github.com/moezdil/siltide/actions/workflows/ci.yml/badge.svg" alt="ci"></a>
  <a href="https://github.com/moezdil/siltide/releases"><img src="https://img.shields.io/github/v/release/moezdil/siltide?include_prereleases&sort=semver" alt="release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/moezdil/siltide" alt="go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="license"></a>
  <a href="https://github.com/sponsors/moezdil"><img src="https://img.shields.io/badge/sponsor-%E2%9D%A4-db61a2?logo=githubsponsors&logoColor=white" alt="sponsor"></a>
</p>

siltide is a terminal monitor for AI accelerators from 15 vendors: GPUs, NPUs, XPUs, MLUs, DCUs, GCUs, and Apple silicon. It shows utilization, memory, processes, power, thermals, links, and health per device. History stays on disk. Pods and Slurm jobs sit next to processes. JSON and Prometheus cover fleets.

More on [the site](https://moezdil.github.io/siltide/): every tab, every key, and screenshots.

<p align="center">
  <img src="assets/demo.gif" alt="siltide moving through the Overview, Devices, Processes, History, Links, Health, and Dashboard tabs on a simulated fleet, with a filter typed into the bar and history scrubbed back" width="100%">
</p>

## Supported accelerators

The vendor list follows the device plugins in [HAMi](https://github.com/Project-HAMi/HAMi/tree/master/pkg/device), plus Apple and Intel. Every vendor is auto-detected. `--vendors nvidia,ascend` limits the probe. NVIDIA (an H100 SXM, driver 570.211.01, every metric checked against `nvidia-smi`) and Apple silicon (an M4 Pro) run on real hardware today. The rest are built against each vendor's documented tool output, with a fixture behind every parser. A hardware report through [an issue](https://github.com/moezdil/siltide/issues/new/choose) is the fastest way to move one from "should work" to confirmed.

**Per-process** is the column worth reading first: whether the vendor's tool
names the processes holding a device, not just the device totals. It is what
turns "this GPU is at 90%" into "this job is holding it". Where it says no,
the vendor's own tool does not report it, so neither do we.

| Vendor | Devices | Source | Per-process |
|---|---|---|---|
| NVIDIA | GPUs, MIG slices | NVML loaded with `dlopen` (no cgo), Xid events, NVLink, ECC, row remap, PCIe AER via sysfs | yes, with `/proc` enrichment |
| Apple | Apple silicon GPU | `ioreg`, IOReport (power, energy), SMC (temperature), AGX user clients (per-process GPU time) | yes |
| AMD | Instinct, Radeon | sysfs (`/sys/class/drm`) and DRM `fdinfo` | yes |
| Intel | Data Center GPU, Arc | sysfs and DRM `fdinfo` | yes |
| Huawei Ascend | NPUs | `npu-smi info` | yes |
| AWS | Inferentia, Trainium | `neuron-ls`, `neuron-monitor` | no |
| Cambricon | MLUs | `cnmon` | yes |
| Enflame | GCUs | `efsmi` | no |
| Hygon | DCUs | `hy-smi` (JSON) | no |
| Iluvatar CoreX | GPUs | `ixsmi -q -x` (XML) | no |
| Kunlunxin | XPUs | `xpu_smi` | no |
| MetaX | GPUs | `mx-smi` | no |
| Moore Threads | GPUs | `mthreads-gmi` | no |
| Biren | GPUs | `brsmi` | no |
| VastAI | VA series | PCI sysfs | no |

Fixture sources: [`testdata/SOURCES`](internal/provider/smi/testdata/SOURCES). A value is measured or shown as `N/A`, never guessed. A tool whose output fails to parse shows its error in the Health tab.

## Install

The short version, with every platform and the verification steps in
[docs/INSTALL.md](docs/INSTALL.md):

```sh
# Download the installer, then run it. Two steps rather than curl | sh, so
# you can read the script first. It picks the build for this machine, checks
# it against the published checksums, and installs into /usr/local/bin
# (or ~/.local/bin if that needs a password).
curl -fsSLO https://raw.githubusercontent.com/moezdil/siltide/main/packaging/install/install.sh
sh install.sh

brew install moezdil/tap/siltide              # macOS and Linux
go install github.com/moezdil/siltide@latest  # from source
nix run github:moezdil/siltide -- --demo      # without installing anything
```

## Using it

```sh
siltide                      # interactive terminal UI, vendors auto-detected
siltide --demo               # explore every view with a simulated fleet
siltide --once --json        # one snapshot on stdout
siltide --service --listen :9800   # headless, with the API and /metrics
siltide --mcp-stdio          # answer an agent over the Model Context Protocol
siltide --update             # move to the newest release, checksum verified
```

`?` in the interface lists every tab, key and filter. The rest is in
[docs/USAGE.md](docs/USAGE.md).

## Documentation

| | |
| --- | --- |
| [docs/INSTALL.md](docs/INSTALL.md) | every platform, verifying a download, updating, uninstalling |
| [docs/USAGE.md](docs/USAGE.md) | the interface, the filter language, configuration, fleets, alerts, the API |
| [docs/MCP.md](docs/MCP.md) | serving the snapshot to an agent |
| [docs/PERFORMANCE.md](docs/PERFORMANCE.md) | what it costs to run, and the scripts that measure it |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | building, testing, adding a vendor |
| [docs/PLUGINS.md](docs/PLUGINS.md) | the plugin surface: a signed manifest that runs no code |
| [examples/config.yaml](examples/config.yaml) | every configuration key, with its default |

A [systemd unit](deploy/systemd/siltide.service), a [Kubernetes DaemonSet](deploy/kubernetes/daemonset.yaml) and a
[compose stack with Prometheus and Grafana](deploy/compose/) are in `deploy/`.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to add a vendor and what a change needs before it merges. Security reports: [SECURITY.md](SECURITY.md). Roadmap: the [issue list](https://github.com/moezdil/siltide/issues). Changes: [CHANGELOG.md](CHANGELOG.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
