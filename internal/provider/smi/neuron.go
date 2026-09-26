package smi

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

// Neuron reads AWS Inferentia and Trainium devices. `neuron-ls -j` gives the
// inventory; utilization and memory come from a long-running
// `neuron-monitor` that streams one JSON report per period. The tools expose
// no temperature or power.
func Neuron() Spec {
	n := &neuron{}
	return Spec{
		Vendor: device.Neuron, Label: "AWS Inferentia/Trainium (neuron-ls, neuron-monitor)",
		Tools: []string{"neuron-ls", "/opt/aws/neuron/bin/neuron-ls"},
		Hint:  "AWS Inferentia/Trainium needs aws-neuronx-tools, which installs neuron-ls and neuron-monitor.",
		Read:  n.read,
		Close: n.close,
	}
}

type neuronDev struct {
	Index  int    `json:"neuron_device"`
	BDF    string `json:"bdf"`
	Memory uint64 `json:"memory_size"`
	Cores  []int  `json:"neuroncore_ids"`
}

// neuronReport is the part of a `neuron-monitor` report siltide uses.
type neuronReport struct {
	Runtimes []struct {
		Report struct {
			Counters struct {
				InUse map[string]struct {
					Util float64 `json:"neuroncore_utilization"`
				} `json:"neuroncores_in_use"`
			} `json:"neuroncore_counters"`
			Memory struct {
				Used struct {
					Breakdown struct {
						Cores map[string]map[string]uint64 `json:"neuroncore_memory_usage"`
					} `json:"usage_breakdown"`
				} `json:"neuron_runtime_used_bytes"`
			} `json:"memory_used"`
		} `json:"report"`
	} `json:"neuron_runtime_data"`
	Hardware struct {
		Type string `json:"neuron_device_type"` // "inferentia2", "trainium2"
	} `json:"neuron_hardware_info"`
}

type neuron struct {
	mu      sync.Mutex
	devs    []neuronDev
	util    map[int]float64 // per NeuronCore
	mem     map[int]uint64  // per NeuronCore
	seen    bool            // a monitor report arrived
	kind    string          // device type from `neuron-monitor`
	started bool
	cancel  context.CancelFunc
}

func (n *neuron) read(ctx context.Context, path string) ([]device.Device, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.kind == "" {
		n.kind = "Neuron device"
	}
	if n.devs == nil {
		out, err := run(ctx, path, "-j")
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(out, &n.devs); err != nil {
			return nil, fmt.Errorf("neuron-ls json: %w", err)
		}
	}
	if !n.started {
		n.started = true
		mctx, cancel := context.WithCancel(context.Background())
		n.cancel = cancel
		go n.monitor(mctx, filepath.Join(filepath.Dir(path), "neuron-monitor"))
	}
	out := make([]device.Device, 0, len(n.devs))
	for _, d := range n.devs {
		dev := device.New(device.Neuron, d.Index, "AWS "+n.kind, d.BDF, d.BDF)
		if d.Memory > 0 {
			dev.Metrics[device.MemTotal] = float64(d.Memory)
		}
		if n.seen && len(d.Cores) > 0 {
			var util float64
			var mem uint64
			for _, c := range d.Cores {
				util += n.util[c] // cores without work are absent: 0%
				mem += n.mem[c]
			}
			dev.Metrics[device.Util] = util / float64(len(d.Cores))
			dev.Metrics[device.MemUsed] = float64(mem)
		}
		out = append(out, dev)
	}
	return out, nil
}

// monitor keeps `neuron-monitor` running and folds each report into n.
func (n *neuron) monitor(ctx context.Context, path string) {
	if _, err := exec.LookPath(path); err != nil {
		path = "neuron-monitor"
	}
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, path)
		killWithParent(cmd)
		stdout, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err == nil {
			dec := json.NewDecoder(stdout)
			for {
				var rep neuronReport
				if dec.Decode(&rep) != nil {
					break
				}
				n.apply(rep)
			}
			_ = cmd.Wait() // restarted below either way
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func (n *neuron) apply(rep neuronReport) {
	util, mem := map[int]float64{}, map[int]uint64{}
	for _, rt := range rep.Runtimes {
		for k, v := range rt.Report.Counters.InUse {
			if c, err := strconv.Atoi(k); err == nil {
				util[c] = min(100, util[c]+v.Util)
			}
		}
		for k, parts := range rt.Report.Memory.Used.Breakdown.Cores {
			if c, err := strconv.Atoi(k); err == nil {
				for _, b := range parts {
					mem[c] += b
				}
			}
		}
	}
	n.mu.Lock()
	n.util, n.mem, n.seen = util, mem, true
	if t := rep.Hardware.Type; t != "" {
		n.kind = strings.ToUpper(t[:1]) + t[1:]
	}
	n.mu.Unlock()
}

func (n *neuron) close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cancel != nil {
		n.cancel()
	}
}
