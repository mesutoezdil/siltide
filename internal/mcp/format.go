package mcp

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/events"
)

// find resolves what an agent called a device: an index, a label, an id, or
// enough of the name to be unambiguous.
func find(s collect.Snapshot, want string) (device.Device, bool) {
	want = strings.TrimSpace(want)
	if want == "" {
		return device.Device{}, false
	}
	for _, d := range s.Devices {
		if d.Label() == want || d.ID == want {
			return d, true
		}
	}
	if n, err := strconv.Atoi(want); err == nil {
		for _, d := range s.Devices {
			if d.Index == n && d.Node == "" {
				return d, true
			}
		}
	}
	lower := strings.ToLower(want)
	var hit device.Device
	found := 0
	for _, d := range s.Devices {
		if strings.Contains(strings.ToLower(d.Name), lower) {
			hit, found = d, found+1
		}
	}
	return hit, found == 1
}

// rank orders severities so "warning" can mean "warning or worse".
func rank(s events.Severity) int {
	switch s {
	case events.Critical:
		return 3
	case events.Warning:
		return 2
	}
	return 1 // info, and an empty severity, which means everything
}

// metricText writes a value the way the interface does, in the unit the
// metric is in rather than as a bare float.
func metricText(k device.Metric, v float64) string {
	if math.IsNaN(v) {
		return "N/A"
	}
	switch k {
	case device.MemUsed, device.MemTotal:
		return size(v)
	case device.Util, device.Fan, device.MemBandwidth, device.Encoder, device.Decoder,
		device.SMUtil, device.SMActive, device.SMOccupancy, device.TensorActive, device.DRAMActive,
		device.ViolationPower, device.ViolationTherm:
		return fmt.Sprintf("%.0f%%", v)
	case device.Temp, device.MemTemp, device.TempSlowdown, device.TempShutdown:
		return fmt.Sprintf("%.0f°C", v)
	case device.Power, device.PowerCap:
		return fmt.Sprintf("%.0fW", v)
	case device.ClockCore, device.ClockMem:
		return fmt.Sprintf("%.0fMHz", v)
	case device.PCIeRx, device.PCIeTx:
		return size(v) + "/s"
	}
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprintf("%.2f", v)
}

// pct renders a percentage metric, or N/A when the vendor reported none.
func pct(m device.Metrics, k device.Metric) string {
	if v, ok := m.Get(k); ok {
		return fmt.Sprintf("%.0f%%", v)
	}
	return "N/A"
}

// val renders a metric with a unit suffix, or N/A.
func val(m device.Metrics, k device.Metric, unit string) string {
	v, ok := m.Get(k)
	if !ok {
		return "N/A"
	}
	if k == device.MemUsed || k == device.MemTotal {
		return size(v)
	}
	return fmt.Sprintf("%.0f%s", v, unit)
}

// mem is "used/total" for a device, which is the pair anyone asks for.
func mem(d device.Device) string {
	used, ok1 := d.Metrics.Get(device.MemUsed)
	total, ok2 := d.Metrics.Get(device.MemTotal)
	switch {
	case ok1 && ok2:
		return size(used) + "/" + size(total)
	case ok1:
		return size(used)
	}
	return "N/A"
}

// where names what a process belongs to: a pod, a Slurm job, or its command.
func where(p device.Process) string {
	switch {
	case p.Pod != "":
		s := p.Namespace + "/" + p.Pod
		if p.Workload != "" {
			s += " (" + p.Workload + ")"
		}
		return s
	case p.Job != "":
		return "job " + p.Job
	}
	return p.Command
}

func orNA(s string) string {
	if s == "" {
		return "N/A"
	}
	return s
}

func trunc(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}

// bytes writes a size the way a person says it.
func size(b float64) string {
	switch {
	case math.IsNaN(b):
		return "N/A"
	case b >= 1<<40:
		return fmt.Sprintf("%.1fT", b/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", b/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", b/(1<<10))
	}
	return fmt.Sprintf("%.0f", b)
}
