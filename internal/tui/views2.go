package tui

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/host"
	"github.com/moezdil/siltide/internal/kube"
)

// ---- Nodes and Network ----

type nodeRow struct {
	name    string
	local   bool
	devs    []device.Device
	status  string
	healthy bool
}

func (m Model) nodes() []nodeRow {
	byName := map[string]*nodeRow{}
	var order []string
	add := func(name string, local bool) *nodeRow {
		if r, ok := byName[name]; ok {
			return r
		}
		r := &nodeRow{name: name, local: local, status: "ok", healthy: true}
		byName[name] = r
		order = append(order, name)
		return r
	}
	add(m.snap.Host+" (local)", true)
	for _, d := range m.snap.Devices {
		if d.Node == "" {
			add(m.snap.Host+" (local)", true).devs = append(byName[m.snap.Host+" (local)"].devs, d)
			continue
		}
		r := add(d.Node, false)
		r.devs = append(r.devs, d)
	}
	for _, p := range m.snap.Providers {
		if n, ok := strings.CutPrefix(p.Name, "node:"); ok {
			n = strings.SplitN(n, ":", 2)[0]
			r := add(n, false)
			if !p.Active || p.Error != "" {
				r.status, r.healthy = orQ(p.Error), false
			}
		}
	}
	out := make([]nodeRow, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

func (m *Model) viewNodes() string {
	th := m.th
	rows := m.nodes()
	cols := []column{{"NODE", 26, false}, {"DEVICES", 7, true}, {"BUSY", 5, true}, {"IDLE", 5, true}, {"AVG UTIL", 8, true}, {"MEMORY", 20, false}, {"POWER", 8, true}, {"HEALTH", 6, true}, {"ALERTS", 6, true}, {"STATUS", 30, false}}
	var out [][]string
	for _, r := range rows {
		f := fleetOf(r.devs)
		alerts := 0
		for _, a := range m.snap.Alerts {
			for _, d := range r.devs {
				if a.Device == d.ID {
					alerts++
				}
			}
		}
		util := th.dim.Render(na)
		if !math.IsNaN(f.AvgUtil) {
			util = fmt.Sprintf("%.0f%%", f.AvgUtil)
		}
		mem := th.dim.Render(na)
		if f.MemTotal > 0 {
			mem = bytes(f.MemUsed) + "/" + bytes(f.MemTotal)
		}
		power := th.dim.Render(na)
		if f.PowerW > 0 {
			power = fmt.Sprintf("%.0fW", f.PowerW)
		}
		status := th.ok.Render(r.status)
		if !r.healthy {
			status = th.warn.Render(r.status)
		}
		out = append(out, []string{r.name, fmt.Sprint(len(r.devs)), fmt.Sprint(f.Busy), fmt.Sprint(f.Idle), util, mem, power, fmt.Sprintf("%.0f", f.AvgHealth), fmt.Sprint(alerts), status})
	}
	m.headSpans = nil
	var b strings.Builder
	b.WriteString(th.bold.Render("Nodes") + th.dim.Render("  add remote siltide services or ssh hosts under nodes: in the config") + "\n")
	b.WriteString(th.table(cols, out, m.sel, min(len(out), max(m.height/3, 3)), m.width, -1, false) + "\n\n")
	if sched := m.scheduler(); sched != "" {
		b.WriteString(sched + "\n")
	}
	if h := m.snap.Host2; h != nil && (m.sel == 0 || m.sel >= len(rows)) {
		b.WriteString(m.hostDetail(*h))
	} else if m.sel < len(rows) {
		b.WriteString(th.dim.Render("select the local node for host details; remote nodes show their devices above"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// scheduler shows what the cluster believes about each node's accelerators
// next to what siltide measured on the ones it can see. The two disagreeing
// is the whole reason to draw it: the scheduler counts devices it handed out,
// siltide watches what those devices then did.
func (m Model) scheduler() string {
	nodes := m.snap.KubeNodes
	if len(nodes) == 0 {
		return ""
	}
	th := m.th
	cols := []column{
		{"NODE", 26, false}, {"RESOURCE", 22, false}, {"CAPACITY", 8, true}, {"ALLOCATABLE", 11, true},
		{"REQUESTED", 9, true}, {"FREE", 6, true}, {"PODS", 5, true}, {"MEASURED", 34, false},
	}
	var rows [][]string
	for _, n := range nodes {
		for _, name := range sortedResourceNames(n.Resources) {
			r := n.Resources[name]
			node := n.Name
			if n.Unschedulable {
				node = th.warn.Render(n.Name + " (cordoned)")
			}
			rows = append(rows, []string{
				node, name, fmt.Sprint(r.Capacity), fmt.Sprint(r.Allocatable),
				fmt.Sprint(r.Requested), fmt.Sprint(r.Free()), fmt.Sprint(r.Pods),
				m.measuredAgainst(n.Name, r),
			})
		}
	}
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(th.bold.Render("What the scheduler believes") +
		th.dim.Render("  capacity and requests from the API server, measured from the devices themselves") + "\n")
	b.WriteString(th.table(cols, rows, -1, min(len(rows), max(m.height/4, 3)), m.width, -1, false))
	return b.String()
}

// measuredAgainst says what siltide sees on a node the scheduler has handed
// devices out on, when it can see that node at all.
func (m Model) measuredAgainst(node string, r kube.NodeResource) string {
	th := m.th
	var devs []device.Device
	for _, d := range m.snap.Devices {
		if d.Node == node || (d.Node == "" && strings.HasPrefix(m.snap.Host, node)) {
			devs = append(devs, d)
		}
	}
	if len(devs) == 0 {
		return th.dim.Render("no devices from this node reach siltide")
	}
	idle := 0
	for _, d := range devs {
		if d.State == device.StateIdle || d.IdleAlloc {
			idle++
		}
	}
	switch {
	case r.Requested > 0 && idle > 0:
		return th.warn.Render(fmt.Sprintf("%d handed out, %d doing nothing", r.Requested, idle))
	case r.Requested > 0:
		return th.ok.Render(fmt.Sprintf("%d handed out, all working", r.Requested))
	}
	return th.dim.Render(fmt.Sprintf("%d devices here, none requested", len(devs)))
}

// sortedResourceNames keeps the table in one order between refreshes.
func sortedResourceNames(m map[string]kube.NodeResource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m Model) hostDetail(h host.Stats) string {
	th := m.th
	var b strings.Builder
	up := ""
	if h.Uptime > 0 {
		up = "  up " + (time.Duration(h.Uptime) * time.Second).Truncate(time.Minute).String()
	}
	fmt.Fprintf(&b, "%s  %s%s\n", th.bold.Render(h.Hostname), th.dim.Render(h.OS), th.dim.Render(up))
	cpu := th.dim.Render(na)
	if !h.CPU.Percent.Unknown() {
		cpu = th.pctBar(float64(h.CPU.Percent), true, 20)
	}
	load := th.dim.Render(na)
	if !h.CPU.Load1.Unknown() {
		load = fmt.Sprintf("%.2f %.2f %.2f", float64(h.CPU.Load1), float64(h.CPU.Load5), float64(h.CPU.Load15))
	}
	mhz := ""
	if !h.CPU.MHz.Unknown() {
		mhz = fmt.Sprintf("  %.0f MHz", float64(h.CPU.MHz))
	}
	fmt.Fprintf(&b, "%-8s %s  %s %d cores  %s load %s%s\n", "cpu", cpu, th.dim.Render("·"), h.CPU.Cores, th.dim.Render("·"), load, th.dim.Render(mhz))
	if perLine := max((m.width-10)/14, 1); len(h.CPU.PerCore) > 4*perLine {
		// many cores: one cell per core, so a 224-thread host takes 2 lines
		cells := []rune(" ▁▂▃▄▅▆▇█")
		var line strings.Builder
		for i, c := range h.CPU.PerCore {
			v := min(max(float64(c), 0), 100)
			line.WriteString(th.level(v).Render(string(cells[int(v/100*float64(len(cells)-1))])))
			if (i+1)%(m.width-10) == 0 || i == len(h.CPU.PerCore)-1 {
				b.WriteString("         " + line.String() + "\n")
				line.Reset()
			}
		}
	} else if len(h.CPU.PerCore) > 0 {
		var cores []string
		for i, c := range h.CPU.PerCore {
			v := float64(c)
			cores = append(cores, fmt.Sprintf("%2d %s", i, th.bar(v, 8)))
			if (i+1)%perLine == 0 || i == len(h.CPU.PerCore)-1 {
				b.WriteString("         " + strings.Join(cores, " ") + "\n")
				cores = nil
			}
		}
	}
	if !h.Mem.Total.Unknown() && h.Mem.Total > 0 {
		pct := float64(h.Mem.Used) / float64(h.Mem.Total) * 100
		fmt.Fprintf(&b, "%-8s %s %s/%s", "memory", th.pctBar(pct, true, 20), bytes(float64(h.Mem.Used)), bytes(float64(h.Mem.Total)))
		if h.Mem.SwapTotal > 0 {
			fmt.Fprintf(&b, "  %s swap %s/%s", th.dim.Render("·"), bytes(float64(h.Mem.SwapUsed)), bytes(float64(h.Mem.SwapTotal)))
		}
		b.WriteString("\n")
	}
	if len(h.Filesystems) > 0 {
		b.WriteString("\n" + th.dim.Render(fmt.Sprintf("%-28s %-8s %-22s %s", "FILESYSTEM", "TYPE", "USED", "")) + "\n")
		for i, fs := range h.Filesystems {
			if i >= 6 {
				b.WriteString(th.dim.Render(fmt.Sprintf("… %d more", len(h.Filesystems)-i)) + "\n")
				break
			}
			pct := 0.0
			if fs.Total > 0 {
				pct = fs.Used / fs.Total * 100
			}
			fmt.Fprintf(&b, "%-28s %-8s %s %s/%s\n", trunc(fs.Mount, 28), fs.Type, th.pctBar(pct, true, 12), bytes(fs.Used), bytes(fs.Total))
		}
	}
	if len(h.Disks) > 0 {
		b.WriteString("\n" + th.dim.Render(fmt.Sprintf("%-12s %10s %10s %8s %8s %6s", "DISK", "READ", "WRITE", "R IOPS", "W IOPS", "BUSY")) + "\n")
		for i, d := range h.Disks {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&b, "%-12s %10s %10s %8.0f %8.0f %5.0f%%\n", d.Name, rate(d.ReadBps), rate(d.WriteBps), d.ReadIOPS, d.WriteIOPS, d.Busy)
		}
	}
	return b.String()
}

type netRow struct {
	name, kind, state string
	rx, tx, rxp, txp  float64
	errs, drops       uint64
	rateStr           string
}

func (m Model) netRows() []netRow {
	h := m.snap.Host2
	if h == nil {
		return nil
	}
	var out []netRow
	for _, n := range h.Nets {
		state := "down"
		if n.Up {
			state = "up"
		}
		out = append(out, netRow{name: n.Name, kind: "ethernet", state: state, rx: n.RxBps, tx: n.TxBps, rxp: n.RxPackets, txp: n.TxPackets, errs: n.RxErrors + n.TxErrors, drops: n.RxDrops + n.TxDrops})
	}
	for _, p := range h.IB {
		out = append(out, netRow{name: fmt.Sprintf("%s:%d", p.Device, p.Port), kind: "infiniband", state: strings.ToLower(p.State), rx: p.RxBps, tx: p.TxBps, errs: p.Errors, rateStr: p.Rate})
	}
	return out
}

func (m *Model) viewNetwork() string {
	th := m.th
	rows := m.netRows()
	if len(rows) == 0 {
		return "no network interfaces reported on this host"
	}
	cols := []column{{"INTERFACE", 18, false}, {"KIND", 10, false}, {"STATE", 8, false}, {"RX", 11, true}, {"TX", 11, true}, {"RX PPS", 8, true}, {"TX PPS", 8, true}, {"ERRORS", 7, true}, {"DROPS", 7, true}, {"LINK", 22, false}}
	var out [][]string
	for _, r := range rows {
		state := th.ok.Render(r.state)
		if r.state != "up" && r.state != "active" {
			state = th.dim.Render(r.state)
		}
		errs, drops := fmt.Sprint(r.errs), fmt.Sprint(r.drops)
		if r.errs > 0 {
			errs = th.warn.Render(errs)
		}
		if r.drops > 0 {
			drops = th.mid.Render(drops)
		}
		out = append(out, []string{r.name, r.kind, state, rate(r.rx), rate(r.tx), fmt.Sprintf("%.0f", r.rxp), fmt.Sprintf("%.0f", r.txp), errs, drops, r.rateStr})
	}
	m.headSpans = nil
	return th.bold.Render("Network") + th.dim.Render("  rates since the previous sample; InfiniBand from sysfs port counters") + "\n" + th.table(cols, out, m.sel, m.height-6, m.width, -1, false)
}

// ---- Kubernetes and workloads ----

type podRow struct {
	ns, name, workload string
	containers         map[string]bool
	devs               map[string]bool
	mem, util          float64
	procs              int
	idle               int // devices idle but allocated
	held               int // devices allocated without a process
	price              float64
}

func (m Model) pods() []podRow {
	by := map[string]*podRow{}
	var order []string
	get := func(ns, name, workload string) *podRow {
		key := ns + "/" + name
		r, ok := by[key]
		if !ok {
			r = &podRow{ns: ns, name: name, workload: workload, containers: map[string]bool{}, devs: map[string]bool{}}
			by[key] = r
			order = append(order, key)
		}
		return r
	}
	prices := m.eng.Prices()
	for _, d := range m.devices() {
		if a := d.Allocated; strings.Contains(a, "/") && !strings.HasPrefix(a, "job ") {
			ns, name, _ := strings.Cut(a, "/")
			r := get(ns, name, "")
			if !r.devs[d.ID] {
				r.devs[d.ID] = true
				r.price += prices.Price(d)
				if len(d.Procs) == 0 {
					r.held++
				}
			}
		}
		for _, p := range d.Procs {
			if p.Pod == "" {
				continue
			}
			r := get(p.Namespace, p.Pod, p.Workload)
			if r.workload == "" {
				r.workload = p.Workload
			}
			r.containers[p.Container] = true
			if !r.devs[d.ID] {
				r.devs[d.ID] = true
				r.price += prices.Price(d)
				if d.IdleAlloc {
					r.idle++
				}
			}
			r.mem += p.Metrics.Or(device.MemUsed, 0)
			r.util += p.Metrics.Or(device.Util, 0)
			r.procs++
		}
	}
	sort.Strings(order)
	out := make([]podRow, 0, len(order))
	for _, k := range order {
		r := *by[k]
		if m.ns != "" && r.ns != m.ns {
			continue
		}
		if !m.filter.Empty() && !m.filter.Words(r.name, r.ns, r.workload) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// unreachable explains an empty view by listing what was looked for and
// where. A tab that says only that something is missing leaves the reader to
// guess which assumption was wrong; this is the same account --diagnose
// prints, in the tab where the data would have been.
func (m Model) unreachable(headline string, attempts []kube.Attempt) string {
	th := m.th
	var b strings.Builder
	b.WriteString(th.bold.Render(headline) + "\n")
	if len(attempts) == 0 {
		return b.String()
	}
	b.WriteString(th.dim.Render("what was tried:") + "\n")
	w := max(m.width, 40)
	for _, a := range attempts {
		b.WriteString("  " + pad(a.What, 28) + trunc(a.Where, max(w-32, 12)) + "\n")
		// The reason gets its own line: it is the part worth reading, and on a
		// narrow terminal it is the first thing a third column would cut.
		if a.Err == "" {
			b.WriteString("    " + th.ok.Render("ok") + "\n")
			continue
		}
		b.WriteString("    " + th.dim.Render(trunc(a.Err, w-6)) + "\n")
	}
	return b.String()
}

func (m *Model) viewKube() string {
	th := m.th
	rows := m.pods()
	if len(rows) == 0 {
		if !m.snap.Kube && !m.snap.Demo {
			return m.unreachable("Kubernetes was not detected.", m.eng.Kube().Attempts())
		}
		if m.ns != "" {
			return "no pods in namespace " + m.ns + " use accelerators"
		}
		return "no pods are using accelerators"
	}
	cols := []column{{"NAMESPACE", 16, false}, {"POD", 36, false}, {"CONTAINERS", 12, false}, {"DEVICES", 7, true}, {"HELD", 4, true}, {"PROCS", 5, true}, {"MEM", 9, true}, {"UTIL", 6, true}, {"IDLE", 4, true}, {"WORKLOAD", 30, false}}
	var out [][]string
	for _, r := range rows {
		var cs []string
		for c := range r.containers {
			cs = append(cs, c)
		}
		sort.Strings(cs)
		idle, held := "", ""
		if r.idle > 0 {
			idle = th.mid.Render(fmt.Sprint(r.idle))
		}
		if r.held > 0 {
			held = th.info.Render(fmt.Sprint(r.held))
		}
		out = append(out, []string{r.ns, r.name, strings.Join(cs, ","), fmt.Sprint(len(r.devs)), held, fmt.Sprint(r.procs), bytes(r.mem), fmt.Sprintf("%.0f%%", r.util), idle, r.workload})
	}
	m.headSpans = nil
	src := m.snap.KubeFrom
	if src == "" {
		src = "processes"
	}
	return th.bold.Render("Pods on accelerators") + th.dim.Render("  source: "+src+"  ·  held = allocated through the kubelet with no process yet  ·  "+m.markNote()+"⏎ describe, l logs, x marks a row") + "\n" +
		th.tableMarks(cols, out, m.sel, m.height-6, m.width, -1, false, m.markedRows())
}

func (m *Model) describe() {
	rows := m.pods()
	if m.sel >= len(rows) {
		return
	}
	r := rows[m.sel]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, err := m.eng.Kube().Describe(ctx, r.ns, r.name)
	if err != nil {
		text = "describe: " + err.Error() + "\n\nWhat siltide knows from processes:\n"
	}
	var b strings.Builder
	b.WriteString(text)
	b.WriteString("\nAccelerators:\n")
	for _, d := range m.snap.Devices {
		if !r.devs[d.ID] {
			continue
		}
		fmt.Fprintf(&b, "  #%-6s %-28s util %s mem %-14s %s\n", d.Label(), trunc(d.Name, 28), pad(m.th.opt(d.Metrics, device.Util), 5), memText(d.Metrics), m.state(d))
		for _, p := range d.Procs {
			if p.Pod == r.name {
				fmt.Fprintf(&b, "     pid %-7d %-12s %-8s mem %s\n", p.PID, trunc(p.Name, 12), p.Container, m.th.opt(p.Metrics, device.MemUsed))
			}
		}
	}
	m.text, m.textTitle, m.overlay, m.scroll = b.String(), "describe "+r.ns+"/"+r.name, overlayDescribe, 0
}

func (m *Model) logs() {
	rows := m.pods()
	if m.sel >= len(rows) {
		return
	}
	r := rows[m.sel]
	var cs []string
	for c := range r.containers {
		cs = append(cs, c)
	}
	sort.Strings(cs)
	container := ""
	if len(cs) > 0 {
		container = cs[m.container%len(cs)]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, err := m.eng.Kube().Logs(ctx, r.ns, r.name, container, 500)
	if err != nil {
		text = "logs: " + err.Error()
	}
	title := "logs " + r.ns + "/" + r.name
	if container != "" {
		title += " " + container
	}
	m.text, m.textTitle, m.overlay = text, title, overlayLogs
	lines := strings.Count(text, "\n")
	m.scroll = max(lines-(m.height-5), 0) // start at the end
}

func (m Model) viewOverlay() string {
	th := m.th
	if m.overlay == overlayCompare {
		return m.viewCompare()
	}
	var b strings.Builder
	b.WriteString(th.bold.Render(m.textTitle) + th.dim.Render(fmt.Sprintf("  line %d", m.scroll+1)) + "\n")
	lines := strings.Split(m.text, "\n")
	if m.wrap {
		// A window this narrow cannot hold a cut and its ellipsis, and a cut
		// that takes no runes off the line would wrap it forever.
		w := max(m.width, 8)
		var wrapped []string
		for _, l := range lines {
			for width(l) > w {
				wrapped = append(wrapped, trunc(l, w))
				l = string([]rune(l)[min(w-1, len([]rune(l))):])
			}
			wrapped = append(wrapped, l)
		}
		lines = wrapped
	}
	start := min(m.scroll, max(len(lines)-1, 0))
	end := min(start+max(m.height-4, 1), len(lines)) // one line always shows
	for _, l := range lines[start:max(end, start)] {
		b.WriteString(l + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

type workloadRow struct {
	name       string
	pods, devs map[string]bool
	mem, util  float64
	idle, held int
	price      float64
}

func (m Model) workloads() []workloadRow {
	by := map[string]*workloadRow{}
	var order []string
	for _, p := range m.pods() {
		name := p.workload
		if name == "" {
			name = "Pod/" + p.name
		}
		r, ok := by[name]
		if !ok {
			r = &workloadRow{name: name, pods: map[string]bool{}, devs: map[string]bool{}}
			by[name] = r
			order = append(order, name)
		}
		r.pods[p.ns+"/"+p.name] = true
		for d := range p.devs {
			r.devs[d] = true
		}
		r.mem += p.mem
		r.util += p.util
		r.idle += p.idle
		r.held += p.held
		r.price += p.price
	}
	// Slurm jobs and bare users become workloads too
	for _, d := range m.devices() {
		for _, p := range d.Procs {
			if p.Pod != "" {
				continue
			}
			name := "user/" + orQ(p.User)
			if p.Job != "" {
				name = "job/" + p.Job
				if p.Workload != "" {
					name = p.Workload
				}
			}
			r, ok := by[name]
			if !ok {
				r = &workloadRow{name: name, pods: map[string]bool{}, devs: map[string]bool{}}
				by[name] = r
				order = append(order, name)
			}
			if !r.devs[d.ID] {
				r.devs[d.ID] = true
				r.price += m.eng.Prices().Price(d)
				if d.IdleAlloc {
					r.idle++
				}
			}
			r.mem += p.Metrics.Or(device.MemUsed, 0)
			r.util += p.Metrics.Or(device.Util, 0)
		}
	}
	sort.Strings(order)
	out := make([]workloadRow, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

func (m *Model) viewWorkloads() string {
	th := m.th
	rows := m.workloads()
	if len(rows) == 0 {
		return "no workloads are using accelerators"
	}
	cols := []column{{"WORKLOAD", 36, false}, {"PODS", 5, true}, {"DEVICES", 7, true}, {"HELD", 4, true}, {"MEM", 9, true}, {"AVG UTIL", 8, true}, {"IDLE-ALLOC", 10, true}, {"EFFICIENCY", 14, false}, {"SPEND/H", 9, true}, {"WASTE/H", 9, true}, {"KWH", 8, true}, {"CO2", 8, true}}
	var out [][]string
	var spend, waste float64
	for _, r := range rows {
		kwh, co2 := 0.0, 0.0
		for id := range r.devs {
			if en, ok := m.snap.Energy[id]; ok {
				kwh += en.KWh
				co2 += en.CO2g
			}
		}
		kwhS, co2S := "", ""
		if kwh > 0 {
			kwhS = fmt.Sprintf("%.3f", kwh)
		}
		if co2 > 0 {
			co2S = fmt.Sprintf("%.0fg", co2)
		}
		n := float64(len(r.devs))
		avg := 0.0
		if n > 0 {
			avg = r.util / n
		}
		eff := th.pctBar(avg, true, 8)
		idle, held := "", ""
		if r.idle > 0 {
			idle = th.warn.Render(fmt.Sprint(r.idle))
		}
		if r.held > 0 {
			held = th.info.Render(fmt.Sprint(r.held))
		}
		sp, ws := "", ""
		if r.price > 0 {
			w := r.price * (100 - avg) / 100
			sp, ws = fmt.Sprintf("%s%.2f", m.currency, r.price), fmt.Sprintf("%s%.2f", m.currency, w)
			spend += r.price
			waste += w
		}
		out = append(out, []string{r.name, fmt.Sprint(len(r.pods)), fmt.Sprint(len(r.devs)), held, bytes(r.mem), fmt.Sprintf("%.0f%%", avg), idle, eff, sp, ws, kwhS, co2S})
	}
	m.headSpans = nil
	footer := ""
	if spend > 0 {
		footer = "\n\n" + fmt.Sprintf("%s %s%.2f/h across priced workloads, %s%.2f/h of it wasted (set cost.per_hour in the config)", th.dim.Render("cost"), m.currency, spend, m.currency, waste)
	}
	return th.bold.Render("Workloads") + th.dim.Render("  grouped by owner; idle-alloc counts devices held without work; kWh and CO2 since siltide started (derived)") + "\n" +
		th.table(cols, out, m.sel, m.height-8, m.width, -1, false) + footer
}

// ---- Dashboard ----

func (m Model) viewDashboard() string {
	th := m.th
	s := m.snap
	f := s.Fleet
	var b strings.Builder
	tiles := []struct{ label, value string }{
		{"devices", fmt.Sprint(f.Devices)},
		{"busy", fmt.Sprint(f.Busy)},
		{"avg util", map[bool]string{true: na, false: fmt.Sprintf("%.0f%%", f.AvgUtil)}[math.IsNaN(f.AvgUtil)]},
		{"power", fmt.Sprintf("%.0fW", f.PowerW)},
		{"memory", map[bool]string{true: na, false: fmt.Sprintf("%.0f%%", f.MemUsed/math.Max(f.MemTotal, 1)*100)}[f.MemTotal == 0]},
		{"max temp", map[bool]string{true: na, false: fmt.Sprintf("%.0f°C", f.MaxTemp)}[math.IsNaN(f.MaxTemp)]},
		{"health", fmt.Sprintf("%.0f", f.AvgHealth)},
		{"alerts", fmt.Sprint(len(s.Alerts))},
	}
	tileW := max(m.width/len(tiles), 10)
	var top, bottom []string
	for _, t := range tiles {
		top = append(top, pad(th.dim.Render(t.label), tileW))
		bottom = append(bottom, pad(th.bold.Render(t.value), tileW))
	}
	b.WriteString(strings.Join(top, "") + "\n" + strings.Join(bottom, "") + "\n\n")
	h := m.eng.History()
	if h == nil {
		return b.String() + "history is disabled: no charts"
	}
	rel := m.reliabilityTable(m.devices())
	from, to := m.histWindow()
	devs := m.devices()
	if m.pick >= 0 && m.pick < len(devs) {
		devs = devs[m.pick : m.pick+1]
	}
	chartW := max(m.width-32, 10)
	linesLeft := m.height - 7
	if rel != "" {
		linesLeft -= strings.Count(rel, "\n") + 3
	}
	fmt.Fprintf(&b, "%s %s → %s   %s %s   %s\n", th.dim.Render("window"), from.Format("15:04"), to.Format("15:04"), th.dim.Render("zoom"), m.window, th.dim.Render("+ - window  { } isolate a device  , . scrub  n now"))
	for _, hm := range histMetrics {
		if linesLeft < 3 {
			break
		}
		b.WriteString(th.bold.Render(hm.name) + "\n")
		linesLeft--
		lw := labelWidth(devs)
		for _, d := range devs {
			if linesLeft < 1 {
				break
			}
			if _, ok := d.Metrics.Get(hm.k); !ok {
				continue
			}
			vals := m.seriesIn(d.ID, hm.k, from, to, chartW)
			if len(vals) == 0 {
				continue
			}
			fmt.Fprintf(&b, "  %-*s %-10s %s %s\n", lw, trunc(d.Label(), lw), trunc(d.Name, 10), th.role("spark").Render(pad(spark(vals, hm.scale(d, vals), chartW), chartW)), rpad(th.opt(d.Metrics, hm.k), 9))
			linesLeft--
		}
	}
	if rel != "" {
		b.WriteString("\n" + rel + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- Health ----

func (m *Model) viewHealth() string {
	th := m.th
	s := m.snap
	var b strings.Builder
	devs := m.devices()
	cols := []column{{"#", 6, false}, {"NAME", 26, false}, {"HEALTH", 6, true}, {"BAND", 9, false}, {"REASONS", max(m.width-52, 20), false}}
	var rows [][]string
	for _, d := range devs {
		band := "healthy"
		switch {
		case d.Health < 25:
			band = th.crit.Render("critical")
		case d.Health < 50:
			band = th.warn.Render("unhealthy")
		case d.Health < 75:
			band = th.mid.Render("degraded")
		case d.Health < 90:
			band = "good"
		}
		rows = append(rows, []string{d.Label(), d.Name, m.health(d), band, strings.Join(d.HealthNotes, "; ")})
	}
	m.headSpans = nil
	b.WriteString(th.bold.Render("Health") + th.dim.Render("  0-100, derived by siltide; every deduction is explained") + "\n")
	b.WriteString(th.table(cols, rows, m.sel, max(min(len(rows), m.height/2-4), 3), m.width, -1, false) + "\n\n")

	if len(s.Anomalies) > 0 {
		b.WriteString(th.bold.Render("Anomalies") + th.dim.Render("  patterns in the last 30 minutes of history") + "\n")
		for _, a := range s.Anomalies {
			fmt.Fprintf(&b, "  %s #%s %s: %s\n", th.mid.Render("≈"), m.labelOf(a.Device), a.Kind, trunc(a.Message, m.width-24))
		}
		b.WriteString("\n")
	}
	b.WriteString(th.bold.Render("Collectors") + "\n")
	for _, p := range s.Providers {
		if !p.Active {
			continue
		}
		state := th.ok.Render("ok")
		if p.Error != "" {
			state = th.warn.Render(trunc(p.Error, 40))
		}
		fmt.Fprintf(&b, "  %-44s %3d devices  %6s  %3d errors  %s\n", trunc(p.Label, 44), p.Devices, p.Latency.Truncate(time.Millisecond), p.Errors, state)
	}
	self := s.Self
	fmt.Fprintf(&b, "\n%s  rss %s  cpu %.1f%%  goroutines %d  last pass %s  passes %d  up %s\n", th.bold.Render("siltide itself"),
		bytes(self.RSS), self.CPU, self.Goroutine, self.Collect.Truncate(time.Millisecond), self.Passes, time.Since(self.Started).Truncate(time.Second))
	if src := s.KubeFrom; src != "" {
		fmt.Fprintf(&b, "%s Kubernetes via %s", th.dim.Render("sources:"), src)
		if s.Slurm {
			b.WriteString(", Slurm")
		}
		b.WriteString("\n")
	}
	if len(s.Warnings) > 0 {
		b.WriteString("\n" + th.bold.Render("Warnings") + "\n")
		for _, w := range s.Warnings {
			b.WriteString("  " + th.mid.Render(trunc(w, m.width-2)) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- Compare ----

func (m Model) viewCompare() string {
	th := m.th
	devs := m.devices()
	find := func(s string) (device.Device, bool) {
		if i, err := strconv.Atoi(s); err == nil && i >= 0 && i < len(devs) {
			return devs[i], true
		}
		for _, d := range devs {
			if d.Label() == s || d.ID == s || strings.Contains(strings.ToLower(d.Name), strings.ToLower(s)) {
				return d, true
			}
		}
		return device.Device{}, false
	}
	a, ok := find(m.compare[0])
	if !ok {
		return "compare: device " + m.compare[0] + " not found"
	}
	var b strings.Builder
	right := m.compare[1]
	var bm device.Metrics
	var title string
	if right == "" || strings.Contains(right, ":") {
		// same device, earlier time
		when := right
		if when == "" {
			when = m.snap.Time.Add(-15 * time.Minute).Format("15:04")
		}
		t, err := time.ParseInLocation("15:04", when, time.Local)
		if err != nil {
			return "compare: time " + right + " must look like 15:04"
		}
		now := m.snap.Time
		t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
		if t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		bm = device.Metrics{}
		if h := m.eng.History(); h != nil {
			if p, ok := h.At(t)[a.ID]; ok {
				for _, hm := range histMetrics {
					if v, ok := p.Get(hm.k); ok {
						bm[hm.k] = v
					}
				}
			}
		}
		title = fmt.Sprintf("%s  now vs %s", a.Name, t.Format("15:04"))
	} else {
		bd, ok := find(right)
		if !ok {
			return "compare: device " + right + " not found"
		}
		bm = bd.Metrics
		title = fmt.Sprintf("#%s %s  vs  #%s %s", a.Label(), a.Name, bd.Label(), bd.Name)
	}
	b.WriteString(th.bold.Render("Compare") + "  " + title + "\n\n")
	keys := map[device.Metric]bool{}
	for k := range a.Metrics {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	var list []device.Metric
	for k := range keys {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	b.WriteString(th.dim.Render(fmt.Sprintf("%-18s %14s %14s %10s", "metric", "A", "B", "delta")) + "\n")
	for _, k := range list {
		av, aok := a.Metrics.Get(k)
		bv, bok := bm.Get(k)
		as, bs, ds := th.dim.Render(na), th.dim.Render(na), ""
		if aok {
			as = metricText(k, av)
		}
		if bok {
			bs = metricText(k, bv)
		}
		if aok && bok && k != device.Throttle {
			d := av - bv
			ds = fmt.Sprintf("%+.0f", d)
			switch {
			case d > 0:
				ds = th.mid.Render(ds)
			case d < 0:
				ds = th.info.Render(ds)
			}
		}
		fmt.Fprintf(&b, "%-18s %14s %14s %10s\n", k, rpad(as, 14), rpad(bs, 14), rpad(ds, 10))
	}
	return strings.TrimRight(b.String(), "\n")
}

func fleetOf(devs []device.Device) fleetLite {
	f := fleetLite{AvgUtil: math.NaN()}
	n := 0
	for _, d := range devs {
		switch d.State {
		case device.StateBusy:
			f.Busy++
		case device.StateIdle:
			f.Idle++
		}
		if u, ok := d.Metrics.Get(device.Util); ok {
			if math.IsNaN(f.AvgUtil) {
				f.AvgUtil = 0
			}
			f.AvgUtil += u
			n++
		}
		f.MemUsed += d.Metrics.Or(device.MemUsed, 0)
		f.MemTotal += d.Metrics.Or(device.MemTotal, 0)
		f.PowerW += d.Metrics.Or(device.Power, 0)
		f.AvgHealth += float64(d.Health)
	}
	if n > 0 {
		f.AvgUtil /= float64(n)
	}
	if len(devs) > 0 {
		f.AvgHealth /= float64(len(devs))
	}
	return f
}

type fleetLite struct {
	Busy, Idle                                    int
	AvgUtil, MemUsed, MemTotal, PowerW, AvgHealth float64
}
