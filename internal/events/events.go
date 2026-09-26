// Package events turns consecutive snapshots into a log of what changed and
// a set of conditions that are true right now.
package events

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

// Severity of an event or alert.
type Severity string

const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// Event is something that happened, or an alert that is active.
type Event struct {
	Time     time.Time `json:"time"`
	Severity Severity  `json:"severity"`
	Kind     string    `json:"kind"`             // "thermal", "ecc", "device", "rule:<name>", ...
	Device   string    `json:"device,omitempty"` // device ID
	Label    string    `json:"label,omitempty"`  // "3" or "node:3"
	Message  string    `json:"message"`
}

// Rule is a user condition; see config.Rule.
type Rule struct {
	Name     string
	Metric   string  // device metric, or "health"
	Op       string  // < <= > >= == !=
	Value    float64 // bytes for memory metrics
	For      time.Duration
	On       string // "", "allocated", "idle"
	Severity Severity
}

// ParseRule reads "util < 10" style clauses; memory values accept K, M, G, and T.
func ParseRule(name, when string, hold time.Duration, on, severity string) (Rule, error) {
	f := strings.Fields(when)
	if len(f) != 3 {
		return Rule{}, fmt.Errorf("rule %q: want \"<metric> <op> <value>\"", when)
	}
	switch f[1] {
	case "<", "<=", ">", ">=", "==", "!=":
	default:
		return Rule{}, fmt.Errorf("rule %q: unknown operator %s", when, f[1])
	}
	v, err := ParseValue(f[2])
	if err != nil {
		return Rule{}, fmt.Errorf("rule %q: %w", when, err)
	}
	sev := Severity(severity)
	if sev == "" {
		sev = Warning
	}
	if name == "" {
		name = when
	}
	return Rule{Name: name, Metric: f[0], Op: f[1], Value: v, For: hold, On: on, Severity: sev}, nil
}

// ParseValue reads a rule or filter value: a number with an optional
// size suffix (K, M, G, T, with or without B) or a percent sign.
func ParseValue(s string) (float64, error) {
	mult := 1.0
	s = strings.TrimSuffix(strings.TrimSuffix(s, "B"), "iB")
	switch {
	case strings.HasSuffix(s, "T"):
		mult, s = 1<<40, strings.TrimSuffix(s, "T")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "%"):
		s = strings.TrimSuffix(s, "%")
	}
	v, err := strconv.ParseFloat(s, 64)
	return v * mult, err
}

func (r Rule) holds(d device.Device) bool {
	switch r.On {
	case "allocated":
		if len(d.Procs) == 0 && d.Allocated == "" {
			return false
		}
	case "idle":
		if len(d.Procs) > 0 {
			return false
		}
	}
	var v float64
	if r.Metric == "health" {
		v = float64(d.Health)
	} else {
		var ok bool
		if v, ok = d.Metrics.Get(device.Metric(r.Metric)); !ok {
			return false
		}
	}
	return Compare(v, r.Op, r.Value)
}

// Compare reports whether "v op want" holds. An unknown operator reads as !=.
func Compare(v float64, op string, want float64) bool {
	switch op {
	case "<":
		return v < want
	case "<=":
		return v <= want
	case ">":
		return v > want
	case ">=":
		return v >= want
	case "==":
		return v == want
	default:
		return v != want
	}
}

// Thresholds tune the rules.
type Thresholds struct {
	TempWarn float64
	Rules    []Rule
}

// Detector keeps the state needed to spot changes.
type Detector struct {
	t    Thresholds
	mu   sync.Mutex
	prev map[string]device.Device
	log  []Event
	cap  int
	// alerts are active conditions keyed by device+kind.
	alerts map[string]Event
	// streak counts consecutive samples a condition held or cleared, so a
	// single noisy sample neither raises nor clears an alert.
	streak map[string]int
	// since is when a rule condition started holding, for `for:`.
	since  map[string]time.Time
	errors map[string]string // provider name: last error
	seeded bool
	// xids are recent Xid codes per device for the health score.
	xids map[string][]xidAt
}

type xidAt struct {
	code int
	sev  Severity
	at   time.Time
}

// debounce is how many consecutive samples flip an alert.
const debounce = 2

// New returns a detector that keeps the last `keep` events.
func New(t Thresholds, keep int) *Detector {
	return &Detector{t: t, prev: map[string]device.Device{}, cap: keep, alerts: map[string]Event{}, streak: map[string]int{},
		since: map[string]time.Time{}, errors: map[string]string{}, xids: map[string][]xidAt{}}
}

// SetThresholds applies reloaded thresholds and rules, keeping the event log
// and the alerts already firing.
func (d *Detector) SetThresholds(t Thresholds) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.t = t
}

// Observe records what changed since the previous snapshot.
func (d *Detector) Observe(now time.Time, devs []device.Device, providerErrors map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	seen := map[string]bool{}
	for _, dev := range devs {
		seen[dev.ID] = true
		prev, known := d.prev[dev.ID]
		if !known && d.seeded {
			d.add(now, Info, "device", dev, "appeared: "+dev.Name)
		}
		d.rules(now, dev, prev, known)
		dev.Metrics = maps.Clone(dev.Metrics) // providers may reuse maps and slices
		dev.Links = slices.Clone(dev.Links)
		dev.Procs = slices.Clone(dev.Procs)
		d.prev[dev.ID] = dev
	}
	for id, dev := range d.prev {
		if !seen[id] {
			d.add(now, Critical, "device", dev, "vanished: "+dev.Name)
			delete(d.prev, id)
			for k := range d.alerts {
				if d.alerts[k].Device == id {
					delete(d.alerts, k)
				}
			}
		}
	}
	for name, err := range providerErrors {
		if d.errors[name] != err {
			d.errors[name] = err
			if err != "" {
				d.log = append(d.log, Event{Time: now, Severity: Warning, Kind: "provider", Message: name + ": " + err})
			}
		}
	}
	d.seeded = true
	if len(d.log) > d.cap {
		d.log = d.log[len(d.log)-d.cap:]
	}
}

func (d *Detector) rules(now time.Time, dev, prev device.Device, known bool) {
	m := dev.Metrics
	temp, ok := m.Get(device.Temp)
	d.alert(now, ok && temp >= d.t.TempWarn, Warning, "thermal", dev, fmt.Sprintf("%.0f°C, warning at %.0f°C", temp, d.t.TempWarn))

	bits := int(m.Or(device.Throttle, 0))
	d.alert(now, bits&device.ThrottleThermal != 0, Warning, "throttle-thermal", dev, "clocks reduced by temperature")
	d.alert(now, bits&device.ThrottlePowerCap != 0, Info, "throttle-power", dev, "clocks held at the power cap")
	d.alert(now, bits&device.ThrottleHW != 0, Critical, "throttle-hw", dev, "hardware slowdown active")
	d.alert(now, dev.Outlier, Warning, "outlier", dev, "well below its siblings")
	d.alert(now, dev.IdleAlloc, Info, "idle-allocated", dev, "has processes but sits idle")
	d.alert(now, m.Or(device.RemapFailed, 0) > 0, Critical, "remap-failed", dev, "row remapping failed: replace the device")
	d.alert(now, m.Or(device.RemapPending, 0) > 0, Warning, "remap-pending", dev, "row remap pending: reset the device to apply it")

	w, ok1 := m.Get(device.PCIeWidth)
	mw, ok2 := m.Get(device.PCIeMaxWidth)
	d.alert(now, ok1 && ok2 && w < mw, Warning, "pcie", dev, fmt.Sprintf("PCIe link x%.0f of x%.0f", w, mw))
	la, ok1 := m.Get(device.LinksActive)
	lt, ok2 := m.Get(device.LinksTotal)
	d.alert(now, ok1 && ok2 && la < lt, Warning, "links-down", dev, fmt.Sprintf("%.0f of %.0f interconnect links down", lt-la, lt))

	for _, r := range d.t.Rules {
		key := dev.ID + "/rule:" + r.Name
		holds := r.holds(dev)
		if holds {
			if _, ok := d.since[key]; !ok {
				d.since[key] = now
			}
		} else {
			delete(d.since, key)
		}
		fire := holds && now.Sub(d.since[key]) >= r.For
		d.alert(now, fire, r.Severity, "rule:"+r.Name, dev, r.Metric+" "+r.Op+" "+strconv.FormatFloat(r.Value, 'g', -1, 64)+" for "+r.For.String())
	}

	if !known {
		return
	}
	for _, k := range []struct {
		m   device.Metric
		sev Severity
		txt string
	}{{device.EccUncorrected, Critical, "uncorrected ECC error(s)"}, {device.EccCorrected, Info, "corrected ECC error(s)"},
		{device.PCIeReplays, Warning, "PCIe replay(s)"}, {device.RetiredPages, Warning, "retired page(s)"}, {device.RemappedRows, Warning, "remapped row(s)"}} {
		cur, ok1 := m.Get(k.m)
		old, ok2 := prev.Metrics.Get(k.m)
		if ok1 && ok2 && cur > old {
			d.add(now, k.sev, string(k.m), dev, fmt.Sprintf("%.0f new %s", cur-old, k.txt))
		}
	}
	for _, l := range dev.Links {
		for _, pl := range prev.Links {
			if pl.Index != l.Index {
				continue
			}
			switch {
			case pl.Active && !l.Active:
				d.add(now, Warning, "link", dev, fmt.Sprintf("link %d went down", l.Index))
			case !pl.Active && l.Active:
				d.add(now, Info, "link", dev, fmt.Sprintf("link %d came up", l.Index))
			case l.Errors > pl.Errors:
				d.add(now, Warning, "link", dev, fmt.Sprintf("link %d: %d new error(s)", l.Index, l.Errors-pl.Errors))
			}
		}
	}
	old := map[int]device.Process{}
	for _, p := range prev.Procs {
		old[p.PID] = p
	}
	cur := map[int]bool{}
	for _, p := range dev.Procs {
		cur[p.PID] = true
		if _, ok := old[p.PID]; !ok {
			d.add(now, Info, "process", dev, fmt.Sprintf("process %d (%s) started", p.PID, name(p)))
		}
	}
	for pid, p := range old {
		if !cur[pid] {
			d.add(now, Info, "process", dev, fmt.Sprintf("process %d (%s) stopped", pid, name(p)))
		}
	}
}

func name(p device.Process) string {
	switch {
	case p.Pod != "":
		return p.Namespace + "/" + p.Pod
	case p.Name != "":
		return p.Name
	}
	return "?"
}

// alert raises or clears a condition after `debounce` consecutive samples
// and logs the transition.
func (d *Detector) alert(now time.Time, active bool, sev Severity, kind string, dev device.Device, msg string) {
	key := dev.ID + "/" + kind
	_, was := d.alerts[key]
	if active == was {
		d.streak[key] = 0
		if was {
			ev := d.alerts[key]
			ev.Message = msg
			d.alerts[key] = ev
		}
		return
	}
	d.streak[key]++
	if d.streak[key] < debounce {
		return
	}
	d.streak[key] = 0
	if active {
		d.alerts[key] = d.add(now, sev, kind, dev, msg)
	} else {
		delete(d.alerts, key)
		d.add(now, Info, kind, dev, "cleared: "+msg)
	}
}

func (d *Detector) add(now time.Time, sev Severity, kind string, dev device.Device, msg string) Event {
	ev := Event{Time: now, Severity: sev, Kind: kind, Device: dev.ID, Label: dev.Label(), Message: msg}
	d.log = append(d.log, ev)
	return ev
}

// External records an event a provider reported on its own: an Xid or an
// error-correcting code (ECC) error.
func (d *Detector) External(now time.Time, sev Severity, kind, deviceID, label, msg string, code int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sev == "" {
		sev = Warning
	}
	d.log = append(d.log, Event{Time: now, Severity: sev, Kind: kind, Device: deviceID, Label: label, Message: msg})
	if kind == "xid" {
		d.xids[deviceID] = append(d.xids[deviceID], xidAt{code, sev, now})
	}
	if len(d.log) > d.cap {
		d.log = d.log[len(d.log)-d.cap:]
	}
}

// RecentXids returns the Xid codes and the worst severity a device raised
// within the window, for the health score.
func (d *Detector) RecentXids(id string, now time.Time, window time.Duration) (codes []int, worst Severity) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var keep []xidAt
	for _, x := range d.xids[id] {
		if now.Sub(x.at) <= window {
			keep = append(keep, x)
			codes = append(codes, x.code)
			if rank[x.sev] > rank[worst] {
				worst = x.sev
			}
		}
	}
	if len(keep) == 0 {
		delete(d.xids, id)
	} else {
		d.xids[id] = keep
	}
	return codes, worst
}

var rank = map[Severity]int{"": -1, Info: 0, Warning: 1, Critical: 2}

// Log returns the events, oldest first.
func (d *Detector) Log() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Event(nil), d.log...)
}

// Alerts returns the active conditions, most severe first.
func (d *Detector) Alerts() []Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Event, 0, len(d.alerts))
	for _, a := range d.alerts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] > rank[out[j].Severity]
		}
		return out[i].Time.Before(out[j].Time)
	})
	return out
}
