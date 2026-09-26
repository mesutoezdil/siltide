// Package dcgm adds NVIDIA profiling metrics through the `dcgmi` tool when
// the DCGM (Data Center GPU Manager) host engine is installed: SM (streaming
// multiprocessor) active, SM occupancy, tensor active, and DRAM active.
// Utilization says a kernel was running; these say whether the silicon was
// busy. Nothing is read when `dcgmi` is absent.
package dcgm

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

// DCGM field IDs for the profiling group.
const (
	fieldSMActive     = 1002
	fieldSMOccupancy  = 1003
	fieldTensorActive = 1004
	fieldDRAMActive   = 1005
)

var fields = []int{fieldSMActive, fieldSMOccupancy, fieldTensorActive, fieldDRAMActive}

// Provider returns the enricher.
func Provider() provider.Provider {
	var path string
	return provider.Provider{
		Name: "dcgm", Label: "NVIDIA DCGM profiling (dcgmi)",
		Hint: "Profiling metrics need DCGM: install datacenter-gpu-manager and start nv-hostengine.",
		Detect: func() error {
			p, err := exec.LookPath("dcgmi")
			if err != nil {
				return errors.New("dcgmi not found")
			}
			path = p
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(ctx, path, "discovery", "-l").CombinedOutput(); err != nil {
				return errors.New("dcgmi cannot reach nv-hostengine: " + strings.TrimSpace(firstLine(string(out))))
			}
			return nil
		},
		Read: func(context.Context) ([]device.Device, error) { return nil, nil },
		Enrich: func(ctx context.Context, devs []device.Device) error {
			ids := make([]string, len(fields))
			for i, f := range fields {
				ids[i] = strconv.Itoa(f)
			}
			out, err := exec.CommandContext(ctx, path, "dmon", "-e", strings.Join(ids, ","), "-c", "1").Output()
			if err != nil {
				return err
			}
			byIndex := Parse(string(out))
			for i := range devs {
				if devs[i].Vendor != device.NVIDIA || devs[i].Node != "" || devs[i].Parent != "" {
					continue
				}
				for k, v := range byIndex[devs[i].Index] {
					devs[i].Metrics[k] = v
				}
			}
			return nil
		},
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Parse reads `dcgmi dmon -e ... -c 1`:
//
//	# Entity  SMACT  SMOCC  TENSO  DRAMA
//	# ID
//	GPU 0     0.912  0.455  0.610  0.380
//
// Values are ratios; N/A is left out.
func Parse(text string) map[int]device.Metrics {
	out := map[int]device.Metrics{}
	var cols []device.Metric
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if f[0] == "#" && len(f) > 2 && f[1] == "Entity" {
			cols = nil
			for _, name := range f[2:] {
				cols = append(cols, map[string]device.Metric{"SMACT": device.SMActive, "SMOCC": device.SMOccupancy, "TENSO": device.TensorActive, "DRAMA": device.DRAMActive}[name])
			}
			continue
		}
		if f[0] != "GPU" || len(f) < 2+len(cols) || len(cols) == 0 {
			continue
		}
		idx, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		m := device.Metrics{}
		for i, c := range cols {
			if c == "" {
				continue
			}
			if v, err := strconv.ParseFloat(f[2+i], 64); err == nil {
				m[c] = v * 100
			}
		}
		out[idx] = m
	}
	return out
}
