// Package collect polls every provider on an interval, enriches what they
// return (process owners, pods, jobs, allocations, states, health, events)
// and keeps the latest snapshot plus history.
package collect

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/cost"
	"github.com/moezdil/siltide/internal/derive"
	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/events"
	"github.com/moezdil/siltide/internal/health"
	"github.com/moezdil/siltide/internal/history"
	"github.com/moezdil/siltide/internal/host"
	"github.com/moezdil/siltide/internal/kube"
	"github.com/moezdil/siltide/internal/notify"
	"github.com/moezdil/siltide/internal/procinfo"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/slurm"
)

// Schema is the JSON snapshot schema version.
const Schema = "siltide.snapshot/v1"

// Status is what became of one provider.
type Status struct {
	Name    string        `json:"name"`
	Label   string        `json:"label"`
	Active  bool          `json:"active"`
	Error   string        `json:"error,omitempty"` // detect or last read failure
	Hint    string        `json:"hint,omitempty"`
	Latency time.Duration `json:"latency_ns"` // last read
	Errors  int           `json:"errors"`     // read failures so far
	Devices int           `json:"devices"`
}

// Self is siltide's own footprint.
type Self struct {
	RSS       float64       `json:"rss_bytes"`
	CPU       float64       `json:"cpu_percent"` // of one core, since the previous pass
	Goroutine int           `json:"goroutines"`
	Collect   time.Duration `json:"collect_ns"` // last full pass
	Passes    int           `json:"passes"`
	Started   time.Time     `json:"started"`
}

// Energy is what a device consumed since siltide started, from the vendor's
// energy counter when there is one, else power integrated over time.
type Energy struct {
	KWh    float64 `json:"kwh"`
	CO2g   float64 `json:"co2_g"`  // 0 when no grid intensity is configured
	Method string  `json:"method"` // "counter" or "integrated"
}

// Snapshot is the state of every device at one moment.
type Snapshot struct {
	Schema    string            `json:"schema"`
	Time      time.Time         `json:"time"`
	Host      string            `json:"host"`
	Demo      bool              `json:"demo"`
	Devices   []device.Device   `json:"devices"`
	Fleet     derive.Fleet      `json:"fleet"`
	Cost      cost.Summary      `json:"cost"`
	Energy    map[string]Energy `json:"energy,omitempty"` // by device ID
	Alerts    []events.Event    `json:"alerts"`
	Anomalies []derive.Anomaly  `json:"anomalies,omitempty"`
	Providers []Status          `json:"providers"`
	Kube      bool              `json:"kubernetes"`        // a pod source exists on this machine
	KubeFrom  string            `json:"kubernetes_source"` // log-dir, in-cluster, kubeconfig
	// KubeNodes is what the cluster believes about accelerators per node:
	// capacity, allocatable and what pods have asked for. It is the
	// scheduler's view, which is a different thing from what the devices on
	// those nodes report, and comparing the two is the point of having it.
	KubeNodes []kube.NodeInfo `json:"kubernetes_nodes,omitempty"`
	Slurm     bool            `json:"slurm"`
	Host2     *host.Stats     `json:"host_stats,omitempty"`
	Self      Self            `json:"self"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// Engine drives the providers.
type Engine struct {
	provs    []provider.Provider
	cfg      config.Config
	demo     bool
	host     string
	hist     *history.Store
	hostS    *host.Sampler
	procs    *procinfo.Cache
	pods     *kube.Resolver
	podRes   *kube.PodResources
	jobs     *slurm.Resolver
	tracker  *derive.Tracker
	detector *events.Detector
	notifier *notify.Notifier
	prices   cost.Table
	started  time.Time
	record   io.Writer

	mu        sync.Mutex
	active    map[string]bool
	status    []Status
	snap      Snapshot
	changed   chan struct{}
	kick      chan struct{}
	cpuPrev   time.Duration
	cpuAt     time.Time
	hostAt    time.Time
	hostLast  host.Stats
	events    chan provider.Event
	warnings  []string
	energy    map[string]*energyState
	trends    map[string]*procTrend
	anomalies []derive.Anomaly
	anomalyAt time.Time
}

// procTrend is the short in-memory history of one process. History on disk is
// per device, so a process trend lives only as long as the process does.
type procTrend struct {
	util []float64
	mem  []float64
}

// ProcTrendLen is how many samples a process trend keeps.
const ProcTrendLen = 60

type energyState struct {
	first, last float64 // counter joules
	kwh         float64 // integrated
	at          time.Time
	counter     bool
}

// New wires an engine. hist may be nil for `--once`.
func New(provs []provider.Provider, cfg config.Config, hist *history.Store, demo bool) *Engine {
	hostname, _ := os.Hostname()
	t := cfg.Thresholds
	rules := alertRules(cfg)
	e := &Engine{
		provs: provs, cfg: cfg, demo: demo, host: hostname, hist: hist,
		hostS:    host.New(),
		procs:    procinfo.New(30 * time.Second),
		pods:     kube.New(kube.Options{Kubeconfig: cfg.Kubernetes.Kubeconfig, Context: cfg.Kubernetes.Context}),
		jobs:     slurm.New(hostname),
		tracker:  derive.New(derive.Thresholds{IdleUtil: t.IdleUtil, BusyUtil: t.BusyUtil, IdleAfter: t.IdleAfter, OutlierDelta: t.OutlierDelta}),
		detector: events.New(events.Thresholds{TempWarn: t.TempWarn, Rules: rules}, 2000),
		notifier: notify.New(notify.Options{Webhook: cfg.Alerts.Webhook, Slack: cfg.Alerts.Slack, Alertmanager: cfg.Alerts.Alertmanager, MinSeverity: cfg.Alerts.MinSeverity, Resend: cfg.Alerts.Resend, Host: hostname}),
		prices:   cost.Table{Currency: cfg.Cost.Currency, PerHour: cfg.Cost.PerHour},
		started:  time.Now(),
		active:   map[string]bool{},
		changed:  make(chan struct{}, 1),
		kick:     make(chan struct{}, 1),
		events:   make(chan provider.Event, 256),
		energy:   map[string]*energyState{},
		trends:   map[string]*procTrend{},
	}
	if !demo {
		e.podRes = kube.NewPodResources()
	}
	if hist != nil && hist.Warning != "" {
		e.warnings = append(e.warnings, hist.Warning)
	}
	e.status = make([]Status, len(provs))
	for i, p := range provs {
		e.status[i] = Status{Name: p.Name, Label: p.Label, Hint: p.Hint}
	}
	return e
}

// alertRules parses the config's alert rules, dropping the ones that do not
// parse; Validate has already reported those.
func alertRules(cfg config.Config) []events.Rule {
	var rules []events.Rule
	for _, r := range cfg.Alerts.Rules {
		if rule, err := events.ParseRule(r.Name, r.When, r.For, r.On, r.Severity); err == nil {
			rules = append(rules, rule)
		}
	}
	return rules
}

// Reconfigure applies a config re-read from disk to a running engine: refresh
// interval, thresholds, alert rules, delivery targets, prices, and carbon
// intensity. Providers, history, and the event log stay as they are, so a
// reload never restarts collection or loses what already happened.
func (e *Engine) Reconfigure(cfg config.Config) {
	rules := alertRules(cfg)
	e.mu.Lock()
	defer e.mu.Unlock()
	t := cfg.Thresholds
	e.cfg = cfg
	e.tracker.SetThresholds(derive.Thresholds{IdleUtil: t.IdleUtil, BusyUtil: t.BusyUtil, IdleAfter: t.IdleAfter, OutlierDelta: t.OutlierDelta})
	e.detector.SetThresholds(events.Thresholds{TempWarn: t.TempWarn, Rules: rules})
	opts := notify.Options{Webhook: cfg.Alerts.Webhook, Slack: cfg.Alerts.Slack, Alertmanager: cfg.Alerts.Alertmanager, MinSeverity: cfg.Alerts.MinSeverity, Resend: cfg.Alerts.Resend, Host: e.host}
	if n := notify.New(opts); n == nil || e.notifier == nil {
		e.notifier = n // a target was added or removed: start fresh
	} else {
		e.notifier.SetOptions(opts) // keep what was already sent
	}
	e.prices = cost.Table{Currency: cfg.Cost.Currency, PerHour: cfg.Cost.PerHour}
}

// Record writes every snapshot as one JSON line to w (see `--record`).
func (e *Engine) Record(w io.Writer) { e.record = w }

// Detect probes every provider once. Absent vendors are recorded, not fatal.
func (e *Engine) Detect() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, p := range e.provs {
		err := p.Detect()
		e.active[p.Name] = err == nil
		e.status[i].Active = err == nil
		if err != nil {
			e.status[i].Error = err.Error()
		}
	}
}

// watch starts the event streams of active providers.
func (e *Engine) watch(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.provs {
		if p.Events != nil && e.active[p.Name] {
			go p.Events(ctx, func(ev provider.Event) {
				select {
				case e.events <- ev:
				default:
				}
			})
		}
	}
}

// Collect reads every active provider once and updates the snapshot.
func (e *Engine) Collect(ctx context.Context) Snapshot {
	type result struct {
		i       int
		devs    []device.Device
		err     error
		latency time.Duration
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan result, len(e.provs))
	e.mu.Lock()
	for i, p := range e.provs {
		if !e.active[p.Name] {
			continue
		}
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			t0 := time.Now()
			devs, err := p.Read(ctx)
			results <- result{i, devs, err, time.Since(t0)}
		}(i, p)
	}
	e.mu.Unlock()
	// host counters change slowly and, on macOS, cost a few execs: every 5s
	if time.Since(e.hostAt) >= 5*time.Second {
		e.hostLast, e.hostAt = e.hostS.Sample(), time.Now()
		if e.demo {
			e.hostLast = host.Demo(e.hostAt, e.host) // never show the real machine in a demo
		}
	}
	hostStats := e.hostLast
	wg.Wait()
	close(results)

	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	snap := Snapshot{Schema: Schema, Time: now, Host: e.host, Demo: e.demo, Kube: e.pods.Enabled(), KubeFrom: e.pods.Source(), Slurm: e.jobs.Enabled(), Host2: &hostStats}
	errs := map[string]string{}
	for r := range results {
		st := &e.status[r.i]
		st.Error, st.Latency, st.Devices = "", r.latency, len(r.devs)
		if r.err != nil {
			st.Error = r.err.Error()
			st.Errors++
		}
		errs[e.provs[r.i].Name] = st.Error
		snap.Devices = append(snap.Devices, r.devs...)
	}
	device.Sort(snap.Devices)
	for i, p := range e.provs {
		if p.Enrich != nil && e.active[p.Name] {
			if err := p.Enrich(ctx, snap.Devices); err != nil {
				e.status[i].Error = err.Error()
				e.status[i].Errors++
			}
		}
	}
	e.enrich(ctx, snap.Devices)
	snap.KubeNodes = e.pods.Nodes(ctx)
	e.tracker.Apply(now, snap.Devices)
	e.trackProcs(snap.Devices)
	e.drainEvents(now, snap.Devices)
	for i := range snap.Devices {
		d := &snap.Devices[i]
		codes, worst := e.detector.RecentXids(d.ID, now, 10*time.Minute)
		d.Health, d.HealthNotes = health.Score(*d, health.Options{TempWarn: e.cfg.Thresholds.TempWarn, Xids: codes, XidWorst: string(worst)})
	}
	e.detector.Observe(now, snap.Devices, errs)
	snap.Alerts = e.detector.Alerts()
	e.notifier.Push(context.Background(), snap.Alerts)
	snap.Fleet = derive.Summarize(snap.Devices)
	snap.Cost = e.prices.Summarize(snap.Devices)
	snap.Energy = e.trackEnergy(now, snap.Devices)
	snap.Providers = append([]Status(nil), e.status...)
	snap.Warnings = append([]string(nil), e.warnings...)
	if fails, err := e.notifier.Status(); fails > 0 && err != nil {
		snap.Warnings = append(snap.Warnings, "alert delivery: "+err.Error())
	}
	if e.hist != nil {
		e.hist.Record(now, snap.Devices)
		if now.Sub(e.anomalyAt) >= 30*time.Second {
			e.anomalies, e.anomalyAt = derive.Anomalies(e.hist, snap.Devices, now, 30*time.Minute), now
		}
		snap.Anomalies = e.anomalies
	}
	snap.Self = e.self(now, time.Since(start))
	e.snap = snap
	if e.record != nil {
		_ = json.NewEncoder(e.record).Encode(snap)
	}
	select {
	case e.changed <- struct{}{}:
	default:
	}
	return snap
}

// procKey identifies one process of one device.
func procKey(devID string, pid int) string { return devID + "/" + strconv.Itoa(pid) }

// trackProcs appends this pass to every live process trend and forgets the
// processes that are gone. The caller holds the lock.
func (e *Engine) trackProcs(devs []device.Device) {
	live := make(map[string]bool, len(e.trends))
	for _, d := range devs {
		for _, p := range d.Procs {
			k := procKey(d.ID, p.PID)
			live[k] = true
			t := e.trends[k]
			if t == nil {
				t = &procTrend{}
				e.trends[k] = t
			}
			t.util = appendSample(t.util, p.Metrics.Or(device.Util, math.NaN()))
			t.mem = appendSample(t.mem, p.Metrics.Or(device.MemUsed, math.NaN()))
		}
	}
	for k := range e.trends {
		if !live[k] {
			delete(e.trends, k)
		}
	}
}

// appendSample adds v and drops the oldest sample past ProcTrendLen.
func appendSample(s []float64, v float64) []float64 {
	if len(s) == ProcTrendLen {
		copy(s, s[1:])
		return append(s[:ProcTrendLen-1], v)
	}
	return append(s, v)
}

// ProcTrend returns up to n recent samples of one process metric, oldest
// first. Only Util and MemUsed are kept; anything else comes back empty.
func (e *Engine) ProcTrend(devID string, pid int, k device.Metric, n int) []float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.trends[procKey(devID, pid)]
	if t == nil || n <= 0 {
		return nil
	}
	var src []float64
	switch k {
	case device.Util:
		src = t.util
	case device.MemUsed:
		src = t.mem
	default:
		return nil
	}
	if len(src) > n {
		src = src[len(src)-n:]
	}
	return append([]float64(nil), src...)
}

// trackEnergy accumulates kWh per device since siltide started.
func (e *Engine) trackEnergy(now time.Time, devs []device.Device) map[string]Energy {
	out := map[string]Energy{}
	g := e.cfg.Carbon.GramsPerKWh
	seen := map[string]bool{}
	for _, d := range devs {
		seen[d.ID] = true
		st := e.energy[d.ID]
		if st == nil {
			st = &energyState{at: now}
			e.energy[d.ID] = st
			if j, ok := d.Metrics.Get(device.Energy); ok {
				st.first, st.last, st.counter = j, j, true
			}
		}
		if j, ok := d.Metrics.Get(device.Energy); ok && st.counter {
			if j < st.last { // counter reset (driver reload)
				st.first = j - (st.last - st.first)
			}
			st.last = j
		} else if p, ok := d.Metrics.Get(device.Power); ok {
			st.kwh += p * now.Sub(st.at).Hours() / 1000
		}
		st.at = now
		en := Energy{Method: "integrated", KWh: st.kwh}
		if st.counter {
			en.Method, en.KWh = "counter", (st.last-st.first)/3.6e6
		}
		en.CO2g = en.KWh * g
		if en.KWh > 0 || st.counter {
			out[d.ID] = en
		}
	}
	for id := range e.energy {
		if !seen[id] {
			delete(e.energy, id)
		}
	}
	return out
}

// drainEvents forwards provider events, Xids and ECC (error-correcting
// code) errors, into the log.
func (e *Engine) drainEvents(now time.Time, devs []device.Device) {
	labels := map[string]string{}
	for _, d := range devs {
		labels[d.ID] = d.Label()
	}
	for {
		select {
		case ev := <-e.events:
			e.detector.External(now, events.Severity(ev.Severity), ev.Kind, ev.DeviceID, labels[ev.DeviceID], ev.Message, ev.Code)
		default:
			return
		}
	}
}

// self measures siltide's own footprint.
func (e *Engine) self(now time.Time, pass time.Duration) Self {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := Self{RSS: float64(ms.Sys), Goroutine: runtime.NumGoroutine(), Collect: pass, Started: e.started}
	e.snap.Self.Passes++
	s.Passes = e.snap.Self.Passes
	if used, ok := cpuTime(); ok {
		if !e.cpuAt.IsZero() && now.After(e.cpuAt) {
			s.CPU = float64(used-e.cpuPrev) / float64(now.Sub(e.cpuAt)) * 100
		}
		e.cpuPrev, e.cpuAt = used, now
	}
	if rss, ok := rssBytes(); ok {
		s.RSS = rss
	}
	return s
}

// enrich adds owner, command, pod, job, and allocation to local devices.
func (e *Engine) enrich(ctx context.Context, devs []device.Device) {
	allocs := e.allocations(ctx)
	jobsByDevice := map[int]slurm.Job{}
	for _, j := range e.jobs.Jobs() {
		for _, idx := range j.Devices {
			jobsByDevice[idx] = j
		}
	}
	for i := range devs {
		d := &devs[i]
		if d.Node != "" {
			continue // the remote siltide already did this
		}
		for j := range d.Procs {
			p := &d.Procs[j]
			info := e.procs.Lookup(p.PID)
			if p.Name == "" && info.Command != "" {
				p.Name = firstWord(info.Command)
			}
			if p.User == "" {
				p.User = info.User
			}
			if p.Command == "" {
				p.Command = info.Command
			}
			if p.Started.IsZero() {
				p.Started = info.Started
			}
			if p.App == nil {
				p.App = info.App
			}
			if pod, ok := e.pods.Lookup(info.PodUID); ok {
				p.Pod, p.Namespace, p.Workload = pod.Name, pod.Namespace, pod.Workload
				p.Container = pod.ContainerName(info.Container)
			} else if len(info.Container) >= 12 {
				p.Container = info.Container[:12]
			}
			if p.Job == "" && info.Cgroup != "" {
				p.Job = slurm.JobFromCgroup(info.Cgroup)
			}
			if p.Job != "" && p.Workload == "" {
				if j, ok := e.jobs.Lookup(p.Job); ok {
					p.Workload = "Job/" + j.Name
					if p.User == "" {
						p.User = j.User
					}
				}
			}
		}
		if a, ok := allocs[d.ID]; ok {
			d.Allocated = a
		} else if a, ok := allocs[strconv.Itoa(d.Index)]; ok && d.Vendor == device.NVIDIA {
			d.Allocated = a
		}
		if j, ok := jobsByDevice[d.Index]; ok && d.Allocated == "" && d.Parent == "" {
			d.Allocated = "job " + j.ID + " (" + j.Name + ")"
		}
	}
}

// allocations maps device IDs (or plain indices) to "namespace/pod" from
// the kubelet's pod-resources API.
func (e *Engine) allocations(ctx context.Context) map[string]string {
	if e.podRes == nil {
		return nil
	}
	list, err := e.podRes.Allocations(ctx)
	if err != nil {
		e.note("pod-resources: " + err.Error())
		return nil
	}
	out := map[string]string{}
	for _, a := range list {
		who := a.Namespace + "/" + a.Pod
		id := a.DeviceID
		// Device plugins register UUIDs (NVIDIA), indices, or bus IDs.
		switch {
		case strings.HasPrefix(id, "GPU-") || strings.HasPrefix(id, "MIG-"):
			out["nvidia-"+id] = who
		default:
			out[id] = who
			for _, v := range []device.Vendor{device.AMD, device.Intel, device.Ascend, device.Kunlunxin, device.Cambricon, device.Enflame, device.Hygon, device.Iluvatar, device.MetaX, device.MThreads, device.Biren, device.VastAI, device.Neuron} {
				out[string(v)+"-"+id] = who
			}
		}
	}
	return out
}

// note records a warning once.
func (e *Engine) note(w string) {
	for _, x := range e.warnings {
		if x == w {
			return
		}
	}
	e.warnings = append(e.warnings, w)
}

func firstWord(s string) string {
	for i, c := range s {
		if c == ' ' {
			return s[:i]
		}
	}
	return s
}

// Run collects until ctx ends. Refresh forces an early pass.
func (e *Engine) Run(ctx context.Context) {
	e.Detect()
	e.watch(ctx)
	e.Collect(ctx)
	every := e.Interval()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.kick:
		}
		e.Collect(ctx)
		if d := e.Interval(); d != every {
			every = d
			t.Reset(every)
		}
	}
}

// Interval is the configured time between passes, which a config reload
// can change while Run is going.
func (e *Engine) Interval() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Refresh
}

// Refresh asks Run for an immediate pass.
func (e *Engine) Refresh() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Changed delivers one signal per new snapshot.
func (e *Engine) Changed() <-chan struct{} { return e.changed }

// Snapshot returns the latest snapshot.
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snap
}

// History returns the store (nil without history).
func (e *Engine) History() *history.Store { return e.hist }

// Events returns the event log, oldest first.
func (e *Engine) Events() []events.Event { return e.detector.Log() }

// Kube exposes the pod resolver for describe and logs.
func (e *Engine) Kube() *kube.Resolver { return e.pods }

// Prices returns the cost table.
func (e *Engine) Prices() cost.Table {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prices
}

// Carbon returns the configured grid intensity.
func (e *Engine) Carbon() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Carbon.GramsPerKWh
}

// Close stops the providers and flushes history.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.provs {
		if p.Close != nil && e.active[p.Name] {
			p.Close()
		}
	}
	if e.hist != nil {
		e.hist.Close()
	}
}
