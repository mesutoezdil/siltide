// Package sim fakes a mixed accelerator fleet for `--demo`, so every view can
// be explored on any machine. Every name carries Suffix.
package sim

import (
	"context"
	"math"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

type model struct {
	vendor device.Vendor
	name   string
	count  int
	memGiB float64
	watts  float64 // 0: the tool reports no power
	temp   bool
	links  int // interconnect links per device
	procs  []proc
	node   string // "" is the local host; the rest look like remote nodes
}

type proc struct {
	name, user, pod, ns, workload, cmd string
}

// Now is the clock the fleet moves on; tools that seed history replace it.
var Now = time.Now

// Suffix marks every simulated device name. The screenshot renderer clears it;
// the DEMO badge in the header stays either way.
var Suffix = " (simulated)"

// Provider returns a demo provider with n NVIDIA-class devices plus one
// small group per other vendor. Device 3 of the NVIDIA group is a straggler
// on a narrow PCIe link; device n-1 is idle but allocated.
func Provider(n int) provider.Provider {
	// The users are the service accounts a real cluster runs these under, not
	// stand-in people: what an operator reads in `siltide --demo` should be
	// what they would read on a node with work on it.
	train := proc{"python", "ml-train", "llama-70b-pretrain-0", "ml", "StatefulSet/llama-70b-pretrain", "python -m torch.distributed.run --nproc_per_node 8 train.py --config configs/llama-70b.yaml"}
	serve := proc{"vllm", "vllm", "chat-api-7d9f8b6c5-x2k9p", "inference", "Deployment/chat-api", "python -m vllm.entrypoints.api_server --model meta-llama/Llama-3.1-70B-Instruct --tensor-parallel-size 8"}
	nb := proc{"python", "jupyter", "notebook-ds-04-0", "notebooks", "StatefulSet/notebook-ds-04", "python -m ipykernel_launcher -f /root/.local/share/jupyter/runtime/kernel-3f1a.json"}
	// Each node runs its own work. One pod cannot be on two nodes at once, so
	// the other vendors get jobs of their own rather than a copy of the H100
	// node's: a fleet where every node shows the same pod name is a fleet
	// nobody has.
	sft := proc{"python", "ml-train", "qwen-32b-sft-0", "ml", "Job/qwen-32b-sft", "python -m torch.distributed.run --nproc_per_node 2 sft.py --base Qwen2.5-32B --lora-rank 64"}
	embed := proc{"tei", "tei", "embed-api-5c8d94f7b-lq4mt", "inference", "Deployment/embed-api", "text-embeddings-router --model-id BAAI/bge-m3 --max-batch-tokens 65536"}
	models := []model{
		{device.NVIDIA, "NVIDIA H100 80GB HBM3", n, 80, 700, true, 18, []proc{train, serve}, ""},
		{device.Ascend, "Ascend 910B3", 2, 64, 310, true, 7, []proc{sft}, "ascend-01"},
		{device.Kunlunxin, "Kunlunxin P800 OAM", 1, 96, 400, true, 0, nil, "xpu-01"},
		{device.Cambricon, "Cambricon MLU370-X8", 1, 48, 250, true, 0, nil, "mlu-01"},
		{device.AMD, "AMD Instinct MI300X", 1, 192, 750, true, 7, []proc{embed}, "mi300-01"},
		{device.Neuron, "AWS Inferentia2", 1, 32, 0, false, 0, nil, "inf2-01"},
		{device.Apple, "Apple M4 Pro 20-core GPU", 1, 48, 0, false, 0, nil, "mac-01"},
	}
	start := Now()
	return provider.Provider{
		Name: "sim", Label: "Simulated fleet",
		Detect: func() error { return nil },
		Read: func(context.Context) ([]device.Device, error) {
			var out []device.Device
			t := Now().Sub(start).Seconds()
			for vi, m := range models {
				for i := 0; i < m.count; i++ {
					load := 0.5 + 0.4*math.Sin(t/540+float64(vi*3+i)) + 0.06*jitter(t, vi*8+i)
					if m.vendor == device.NVIDIA { // a training job: pinned high, straggler aside
						load = 0.93 + 0.05*jitter(t, i)
						if math.Mod(t+300, 600) < 45 { // a checkpoint every 10 minutes
							load = 0.12 + 0.05*jitter(t, i)
						}
					}
					load = min(max(load, 0.01), 0.99)
					d := m.device(i, load, t)
					if m.vendor == device.NVIDIA && i == m.count-1 && m.count > 1 {
						d = m.device(i, 0.01, t) // idle but holding a notebook
						d.Procs = []device.Process{procOf(nb, 9000+i, d.Metrics[device.MemUsed], 0)}
					}
					for k := range d.Procs {
						d.Procs[k].PID += vi * 1000 // one PID space per vendor
					}
					out = append(out, d)
					if m.vendor == device.NVIDIA && i == 6 {
						out = append(out, m.partitions(d)...)
					}
				}
			}
			return out, nil
		},
	}
}

// jitter is a deterministic wobble in [-1, 1] that differs per device.
func jitter(t float64, k int) float64 {
	return math.Sin(t*1.7+float64(k)*13.1) * math.Cos(t*0.37+float64(k))
}

// device builds one device at the given load (0..1).
func (m model) device(i int, load, t float64) device.Device {
	d := device.New(m.vendor, i, m.name+Suffix, "", "")
	d.ID += "-sim"
	d.Source = "simulated"
	d.Node = m.node
	gib := m.memGiB * (1 << 30)
	throttle := 0
	if i == 3 { // the straggler: PCIe trained at x8, held back by power
		load, throttle = load*0.55, device.ThrottlePowerCap
	}
	d.Metrics[device.Util] = math.Round(load * 100)
	d.Metrics[device.MemUsed] = math.Round((0.2 + 0.7*load) * gib)
	if m.vendor == device.NVIDIA && load > 0.05 { // a training job keeps its memory through checkpoints
		d.Metrics[device.MemUsed] = math.Round((0.74 + 0.06*jitter(t, i+50)) * gib)
	}
	d.Metrics[device.MemTotal] = gib
	if m.temp {
		d.Metrics[device.Temp] = math.Round(35 + 40*load)
	}
	if m.watts > 0 {
		d.Metrics[device.Power] = math.Round(m.watts * (0.15 + 0.8*load))
		d.Metrics[device.PowerCap] = m.watts
		d.Metrics[device.Throttle] = float64(throttle)
		d.Metrics[device.ClockCore] = math.Round(1000 + 900*load)
		d.Metrics[device.MemBandwidth] = math.Round(load * 70)
		d.Metrics[device.PCIeGen], d.Metrics[device.PCIeMaxGen] = 5, 5
		d.Metrics[device.PCIeWidth], d.Metrics[device.PCIeMaxWidth] = 16, 16
		d.Metrics[device.PCIeRx] = math.Round(load * 12e9)
		d.Metrics[device.PCIeTx] = math.Round(load * 4e9)
		d.Metrics[device.EccCorrected] = math.Floor(t / 90) // one every 90 s
		d.Metrics[device.EccUncorrected] = 0
		if i == 3 {
			d.Metrics[device.PCIeWidth] = 8
		}
	}
	if m.vendor == device.NVIDIA {
		d.Metrics[device.NUMANode] = float64(i / 4)
		d.Metrics[device.MemTemp] = d.Metrics[device.Temp] + 9
		d.Metrics[device.ClockMem] = 2619
		d.Metrics[device.PState] = 0
		d.Metrics[device.Energy] = math.Round(m.watts*0.8*t) + float64(i)*1e6
		d.Metrics[device.RemappedRows], d.Metrics[device.RetiredPages] = 0, 0
		d.Metrics[device.RemapPending], d.Metrics[device.RemapFailed] = 0, 0
		d.Metrics[device.PCIeReplays] = float64(i * 3 % 7)
		d.Metrics[device.ViolationPower], d.Metrics[device.ViolationTherm] = 0, 0
		if i == 3 {
			d.Metrics[device.ViolationPower] = 37
		}
		if i == 5 {
			d.Metrics[device.RemappedRows] = 2
		}
		d.Topology = map[int]string{}
		for peer := 0; peer < m.count; peer++ {
			switch {
			case peer == i:
			case peer/4 == i/4:
				d.Topology[peer] = "NV18"
			default:
				d.Topology[peer] = "SYS"
			}
		}
	}
	if m.links > 0 {
		d.Metrics[device.LinksTotal] = float64(m.links)
		d.Metrics[device.LinksActive] = float64(m.links)
		if i == 3 {
			d.Metrics[device.LinksActive] = float64(m.links - 1)
		}
	}
	for pi, p := range m.procs {
		share := 1 / float64(len(m.procs))
		d.Procs = append(d.Procs, procOf(p, 4000+10*i+pi, d.Metrics[device.MemUsed]*share, load*100*share))
	}
	return d
}

func procOf(p proc, pid int, mem, util float64) device.Process {
	pr := device.Process{
		PID: pid, Name: p.name, User: p.user, Command: p.cmd,
		Pod: p.pod, Namespace: p.ns, Workload: p.workload, Container: "main",
		Started: time.Now().Add(-time.Duration(pid%7+1) * time.Hour),
		Metrics: device.Metrics{device.MemUsed: math.Round(mem), device.Util: math.Round(util)},
	}
	if p.name == "python" {
		pr.App = map[string]float64{"samples_per_s": math.Round(1500 + util*10), "nccl_gbps": math.Round(180 + util)}
	}
	return pr
}

// partitions splits one device into 2 MIG-style (Multi-Instance GPU) slices.
func (m model) partitions(parent device.Device) []device.Device {
	var out []device.Device
	for i, frac := range []float64{0.5, 0.25} {
		p := device.New(m.vendor, i, m.name+" 3g.40gb"+Suffix, "", "")
		p.ID = parent.ID + "-part" + string(rune('0'+i))
		p.Parent, p.Source = parent.ID, "simulated"
		p.Metrics[device.MemTotal] = math.Round(parent.Metrics[device.MemTotal] * frac)
		p.Metrics[device.MemUsed] = math.Round(parent.Metrics[device.MemUsed] * frac)
		out = append(out, p)
	}
	parent.Metrics[device.Partitions] = float64(len(out))
	return out
}
