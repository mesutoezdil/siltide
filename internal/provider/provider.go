// Package provider defines how siltide talks to one accelerator vendor.
package provider

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Provider reads every device of one vendor. A provider whose hardware or
// driver is absent fails Detect and is never asked to Read.
type Provider struct {
	Name  string // config name: "nvidia", "ascend"
	Label string // shown to people: "NVIDIA (NVML)"
	Hint  string // what to install when Detect fails
	// Detect is cheap: a PATH lookup, a library load, or a sysfs stat.
	Detect func() error
	// Read returns every device with its current metrics.
	Read func(ctx context.Context) ([]device.Device, error)
	// Events streams asynchronous device events until ctx ends: Xids,
	// error-correcting code (ECC) errors.
	// Optional; providers without an event source leave it nil.
	Events func(ctx context.Context, emit func(Event))
	// Enrich adds metrics to devices other providers found, for example
	// Data Center GPU Manager (DCGM) profiling on NVIDIA Management Library
	// (NVML) devices. Optional; an enricher's Read may return nothing.
	Enrich func(ctx context.Context, devs []device.Device) error
	// Close releases background work. Optional.
	Close func()
}

// Event is something a device reported on its own, outside sampling.
type Event struct {
	DeviceID string
	Kind     string // "xid", "ecc", "clock", "power", "mig"
	Code     int    // vendor code, for example the Xid number
	Severity string // "info", "warning", "critical"
	Message  string
}

// ProcName returns the command name of pid from /proc, or "".
func ProcName(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
