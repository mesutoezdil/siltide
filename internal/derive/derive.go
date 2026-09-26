// Package derive computes what no vendor reports: device states, fleet
// totals, stragglers, and idle-but-allocated devices. Everything here is an
// opinion built from measured values and is labelled as such in the UI.
package derive

import (
	"encoding/json"
	"math"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

// Thresholds tune the rules.
type Thresholds struct {
	IdleUtil     float64
	BusyUtil     float64
	IdleAfter    time.Duration
	OutlierDelta float64
}

// Tracker remembers how long each device has been idle.
type Tracker struct {
	t         Thresholds
	idleSince map[string]time.Time
}

// New returns a tracker.
func New(t Thresholds) *Tracker { return &Tracker{t: t, idleSince: map[string]time.Time{}} }

// SetThresholds applies reloaded thresholds, keeping how long each device
// has been idle. Callers hold their own lock.
func (tr *Tracker) SetThresholds(t Thresholds) { tr.t = t }

// Apply fills State, Outlier, and IdleAlloc on every device.
func (tr *Tracker) Apply(now time.Time, devs []device.Device) {
	for i := range devs {
		d := &devs[i]
		util, ok := d.Metrics.Get(device.Util)
		switch {
		case len(d.Metrics) == 0:
			d.State = device.StateDown
		case !ok:
			d.State = device.StateActive
		case util >= tr.t.BusyUtil:
			d.State = device.StateBusy
		case util >= tr.t.IdleUtil:
			d.State = device.StateActive
		default:
			d.State = device.StateIdle
		}
		if d.State == device.StateIdle {
			if _, ok := tr.idleSince[d.ID]; !ok {
				tr.idleSince[d.ID] = now
			}
			d.IdleAlloc = len(d.Procs) > 0 && now.Sub(tr.idleSince[d.ID]) >= tr.t.IdleAfter
		} else {
			delete(tr.idleSince, d.ID)
		}
	}
	tr.outliers(devs)
}

// outliers flags a device whose utilization trails the mean of its siblings
// (same node and model, all with work) by more than the delta. The
// minimum is 3 siblings; 2 cannot tell who is wrong.
func (tr *Tracker) outliers(devs []device.Device) {
	groups := map[string][]int{}
	for i, d := range devs {
		if _, ok := d.Metrics.Get(device.Util); ok && len(d.Procs) > 0 && d.Parent == "" {
			key := d.Node + "/" + d.Name
			groups[key] = append(groups[key], i)
		}
	}
	for _, idx := range groups {
		if len(idx) < 3 {
			continue
		}
		sum := 0.0
		for _, i := range idx {
			sum += devs[i].Metrics[device.Util]
		}
		mean := sum / float64(len(idx))
		for _, i := range idx {
			devs[i].Outlier = devs[i].Metrics[device.Util] < mean-tr.t.OutlierDelta
		}
	}
}

// Fleet sums up a set of devices.
type Fleet struct {
	Devices   int     `json:"devices"`
	Busy      int     `json:"busy"`
	Active    int     `json:"active"`
	Idle      int     `json:"idle"`
	Down      int     `json:"down"`
	Allocated int     `json:"allocated"` // devices with at least one process
	IdleAlloc int     `json:"idle_allocated"`
	Outliers  int     `json:"outliers"`
	AvgUtil   float64 `json:"avg_util"` // NaN when no device reports it
	PowerW    float64 `json:"power_w"`  // sum over devices that report it
	PowerCapW float64 `json:"power_cap_w"`
	MemUsed   float64 `json:"mem_used"`
	MemTotal  float64 `json:"mem_total"`
	MaxTemp   float64 `json:"max_temp"` // NaN when unknown
	AvgHealth float64 `json:"avg_health"`
	// Unused is the utilization headroom across allocated devices in
	// device-equivalents: 4 allocated devices at 25% leave 3 unused.
	Unused float64 `json:"unused_equiv"`
}

// Summarize computes fleet totals.
func Summarize(devs []device.Device) Fleet {
	f := Fleet{Devices: len(devs), AvgUtil: math.NaN(), MaxTemp: math.NaN()}
	utilN, utilSum, health := 0, 0.0, 0
	for _, d := range devs {
		switch d.State {
		case device.StateBusy:
			f.Busy++
		case device.StateActive:
			f.Active++
		case device.StateIdle:
			f.Idle++
		case device.StateDown:
			f.Down++
		}
		if len(d.Procs) > 0 {
			f.Allocated++
			if u, ok := d.Metrics.Get(device.Util); ok {
				f.Unused += (100 - u) / 100
			}
		}
		if d.IdleAlloc {
			f.IdleAlloc++
		}
		if d.Outlier {
			f.Outliers++
		}
		if u, ok := d.Metrics.Get(device.Util); ok {
			utilN++
			utilSum += u
		}
		f.PowerW += d.Metrics.Or(device.Power, 0)
		f.PowerCapW += d.Metrics.Or(device.PowerCap, 0)
		f.MemUsed += d.Metrics.Or(device.MemUsed, 0)
		f.MemTotal += d.Metrics.Or(device.MemTotal, 0)
		if t, ok := d.Metrics.Get(device.Temp); ok && (math.IsNaN(f.MaxTemp) || t > f.MaxTemp) {
			f.MaxTemp = t
		}
		health += d.Health
	}
	if utilN > 0 {
		f.AvgUtil = utilSum / float64(utilN)
	}
	if len(devs) > 0 {
		f.AvgHealth = float64(health) / float64(len(devs))
	}
	return f
}

// MarshalJSON writes unknown averages as null instead of NaN.
func (f Fleet) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"devices": f.Devices, "busy": f.Busy, "active": f.Active, "idle": f.Idle, "down": f.Down,
		"allocated": f.Allocated, "idle_allocated": f.IdleAlloc, "outliers": f.Outliers,
		"avg_util": nanNull(f.AvgUtil), "power_w": f.PowerW, "power_cap_w": f.PowerCapW,
		"mem_used": f.MemUsed, "mem_total": f.MemTotal, "max_temp": nanNull(f.MaxTemp),
		"avg_health": f.AvgHealth, "unused_equiv": f.Unused,
	}
	return json.Marshal(m)
}

// UnmarshalJSON accepts null for the averages.
func (f *Fleet) UnmarshalJSON(b []byte) error {
	type plain Fleet
	var p plain
	p.AvgUtil, p.MaxTemp = math.NaN(), math.NaN()
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*f = Fleet(p)
	return nil
}

func nanNull(v float64) any {
	if math.IsNaN(v) {
		return nil
	}
	return v
}
