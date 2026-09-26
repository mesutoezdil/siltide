package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/events"
)

func deviceColumns(w int, devs []device.Device) []column {
	nameW := 0
	for _, d := range devs {
		nameW = max(nameW, width(d.Name))
	}
	nameW = min(max(nameW, 4), max(w/4, 12))
	barW := min(max((w-nameW-78)/2, 5), 24)
	return []column{{"#", 6, false}, {"VENDOR", 9, false}, {"NAME", nameW, false}, {"UTIL", barW + 5, false},
		{"MEM", barW + 5, false}, {"", 13, false}, {"TEMP", 5, true}, {"POWER", 6, true}, {"HEALTH", 6, true}, {"STATE", 22, false}, {"PROCS", 5, true}}
}

// deviceTable renders the standard device table.
func (m *Model) deviceTable(devs []device.Device, height int) string {
	th := m.th
	cols := deviceColumns(m.width, devs)
	barW := cols[3].w - 5
	var rows [][]string
	for _, d := range devs {
		util, ok := d.Metrics.Get(device.Util)
		memPct, okm := d.MemPercent()
		label := d.Label()
		if d.Parent != "" {
			label = "  └" + label
		}
		rows = append(rows, []string{label, string(d.Vendor), d.Name, th.pctBar(util, ok, barW), th.pctBar(memPct, okm, barW),
			memText(d.Metrics), th.opt(d.Metrics, device.Temp), th.opt(d.Metrics, device.Power), m.health(d), m.state(d), fmt.Sprint(len(d.Procs))})
	}
	m.headSpans = headerSpans(cols)
	return th.tableMarks(cols, rows, m.sel, height, m.width, m.sortCol, m.sortDesc, m.markedRows())
}

// headerSpans maps column x ranges for header clicks.
func headerSpans(cols []column) []span {
	var out []span
	x := 0
	for i, c := range cols {
		out = append(out, span{x, x + c.w, i})
		x += c.w + 1
	}
	return out
}

func (m Model) health(d device.Device) string {
	s := fmt.Sprint(d.Health)
	switch {
	case d.Health < 60:
		return m.th.warn.Render(s)
	case d.Health < 90:
		return m.th.mid.Render(s)
	}
	return s
}

func (m Model) state(d device.Device) string {
	th := m.th
	var parts []string
	switch d.State {
	case device.StateBusy:
		parts = append(parts, th.ok.Render("● busy"))
	case device.StateActive:
		parts = append(parts, th.mid.Render("◐ active"))
	case device.StateIdle:
		parts = append(parts, th.dim.Render("○ idle"))
	case device.StateDown:
		parts = append(parts, th.warn.Render("✗ down"))
	}
	if d.Outlier {
		parts = append(parts, th.warn.Render("outlier"))
	}
	if d.IdleAlloc {
		parts = append(parts, th.mid.Render("idle-alloc"))
	}
	if d.Allocated != "" && len(d.Procs) == 0 {
		parts = append(parts, th.info.Render("held"))
	}
	return strings.Join(parts, " ")
}

func (m Model) viewOverview() string {
	th, s := m.th, m.snap
	devs := m.devices()
	if len(devs) == 0 {
		return m.empty()
	}
	f := s.Fleet
	var b strings.Builder
	fleet := []string{
		fmt.Sprintf("%s %d", th.dim.Render("devices"), f.Devices),
		th.ok.Render(fmt.Sprintf("● %d busy", f.Busy)), th.mid.Render(fmt.Sprintf("◐ %d active", f.Active)),
		th.dim.Render(fmt.Sprintf("○ %d idle", f.Idle)),
	}
	if f.Down > 0 {
		fleet = append(fleet, th.warn.Render(fmt.Sprintf("✗ %d down", f.Down)))
	}
	fleet = append(fleet, fmt.Sprintf("%s %d/%d", th.dim.Render("allocated"), f.Allocated, f.Devices))
	if !math.IsNaN(f.AvgUtil) {
		fleet = append(fleet, fmt.Sprintf("%s %.0f%%", th.dim.Render("avg util"), f.AvgUtil))
	}
	if f.PowerW > 0 {
		p := fmt.Sprintf("%s %.0fW", th.dim.Render("power"), f.PowerW)
		if f.PowerCapW > 0 {
			p += th.dim.Render(fmt.Sprintf("/%.0fW", f.PowerCapW))
		}
		fleet = append(fleet, p)
	}
	if f.MemTotal > 0 {
		fleet = append(fleet, fmt.Sprintf("%s %s/%s", th.dim.Render("memory"), bytes(f.MemUsed), bytes(f.MemTotal)))
	}
	if !math.IsNaN(f.MaxTemp) {
		fleet = append(fleet, fmt.Sprintf("%s %.0f°C", th.dim.Render("max temp"), f.MaxTemp))
	}
	fleet = append(fleet, fmt.Sprintf("%s %.0f", th.dim.Render("health"), f.AvgHealth))
	if h := s.Host2; h != nil && !h.CPU.Percent.Unknown() {
		fleet = append(fleet, fmt.Sprintf("%s %.0f%%", th.dim.Render("host cpu"), float64(h.CPU.Percent)))
	}
	b.WriteString(trunc(strings.Join(fleet, "   "), m.width) + "\n")
	var derived []string
	if f.Outliers > 0 {
		derived = append(derived, th.warn.Render(fmt.Sprintf("⚠ %d outlier(s)", f.Outliers)))
	}
	if f.IdleAlloc > 0 {
		derived = append(derived, th.mid.Render(fmt.Sprintf("%d idle but allocated", f.IdleAlloc)))
	}
	if f.Allocated > 0 {
		derived = append(derived, fmt.Sprintf("%.1f device-equivalents unused", f.Unused))
	}
	if c := s.Cost; c.Priced > 0 {
		derived = append(derived, fmt.Sprintf("%s%.2f/h spend, %s%.2f/h wasted", m.currency, c.SpendPerHour, m.currency, c.WastePerHour+c.IdlePerHour))
	}
	if len(derived) > 0 {
		b.WriteString(trunc(strings.Join(derived, th.dim.Render("  ·  "))+th.dim.Render("   (derived)"), m.width) + "\n")
	} else {
		b.WriteString("\n")
	}
	mm := m
	b.WriteString(mm.deviceTable(devs, m.tableHeight()) + "\n")
	b.WriteString(hr(th, m.width) + "\n")

	left := m.topProcs(devs, 6, m.width/2-1)
	right := m.alertsBox(6, m.width-m.width/2-1)
	b.WriteString(sideBySide(left, right, m.width/2, m.width-m.width/2))
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) empty() string {
	msg := "no accelerators found"
	if m.search != "" || m.node != "" {
		msg = "nothing matches the current filter"
	}
	for _, p := range m.snap.Providers {
		if p.Error != "" {
			msg += "\n  " + p.Label + ": " + p.Error
			if p.Hint != "" {
				msg += "\n    " + p.Hint
			}
		}
	}
	return msg + "\n\nTry `siltide --demo`, or ? for the vendors that were probed."
}

// procLess orders processes by memory, then utilization, then PID, so a
// vendor without per-process memory (Apple) still puts the busy ones first.
func procLess(a, b device.Process) bool {
	if am, bm := a.Metrics[device.MemUsed], b.Metrics[device.MemUsed]; am != bm {
		return am > bm
	}
	if au, bu := a.Metrics[device.Util], b.Metrics[device.Util]; au != bu {
		return au > bu
	}
	return a.PID < b.PID
}

func (m Model) topProcs(devs []device.Device, n, w int) string {
	th := m.th
	var rows []procRow
	for _, d := range devs {
		for _, p := range d.Procs {
			rows = append(rows, procRow{d, p})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return procLess(rows[i].p, rows[j].p) })
	by := " by memory"
	if len(rows) > 0 && rows[0].p.Metrics[device.MemUsed] == 0 {
		by = " by utilization" // no per-process memory on this vendor
	}
	var b strings.Builder
	b.WriteString(th.bold.Render("Top processes") + th.dim.Render(by) + "\n")
	if len(rows) == 0 {
		b.WriteString(th.dim.Render("none") + "\n")
	}
	for i, r := range rows {
		if i >= n {
			break
		}
		where := r.p.Pod
		if where == "" {
			where = r.p.User
		}
		// A value that is styled, N/A above all, carries escapes that a width
		// verb counts as characters, so every padded cell here goes through
		// rpad, which measures what the terminal will show.
		line := fmt.Sprintf("%7d %-12s %-5s %s %s %s", r.p.PID, trunc(r.p.Name, 12), r.d.Label(),
			rpad(th.opt(r.p.Metrics, device.MemUsed), 9), rpad(th.opt(r.p.Metrics, device.Util), 5),
			th.dim.Render(trunc(where, max(w-45, 0))))
		b.WriteString(trunc(line, w) + "\n")
	}
	return b.String()
}

func (m Model) alertsBox(n, w int) string {
	th := m.th
	var b strings.Builder
	b.WriteString(th.bold.Render("Alerts") + th.dim.Render(fmt.Sprintf(" %d active, %d anomalies", len(m.snap.Alerts), len(m.snap.Anomalies))) + "\n")
	shown := 0
	for _, a := range m.snap.Anomalies {
		if shown >= n {
			break
		}
		b.WriteString(trunc(th.mid.Render("≈")+" #"+m.labelOf(a.Device)+" "+a.Message, w) + "\n")
		shown++
	}
	for _, a := range m.snap.Alerts {
		if shown >= n {
			break
		}
		b.WriteString(trunc(m.eventLine(a, false), w) + "\n")
		shown++
	}
	if shown < n {
		evs := m.eng.Events()
		for i := len(evs) - 1; i >= 0 && shown < n; i-- {
			if evs[i].Kind == "provider" || strings.HasPrefix(evs[i].Message, "cleared") || isAlert(m.snap.Alerts, evs[i]) {
				continue
			}
			b.WriteString(trunc(m.eventLine(evs[i], true), w) + "\n")
			shown++
		}
	}
	if shown == 0 {
		b.WriteString(th.dim.Render("all quiet") + "\n")
	}
	return b.String()
}

func (m Model) labelOf(id string) string {
	for _, d := range m.snap.Devices {
		if d.ID == id {
			return d.Label()
		}
	}
	return id
}

func isAlert(alerts []events.Event, e events.Event) bool {
	for _, a := range alerts {
		if a.Device == e.Device && a.Kind == e.Kind {
			return true
		}
	}
	return false
}

func (m Model) eventLine(e events.Event, withTime bool) string {
	th := m.th
	mark := th.dim.Render("•")
	switch e.Severity {
	case events.Warning:
		mark = th.warn.Render("▲")
	case events.Critical:
		mark = th.crit.Render("✖")
	}
	t := ""
	if withTime {
		t = th.dim.Render(e.Time.Format("15:04:05")) + " "
	}
	where := ""
	if e.Label != "" {
		where = "#" + e.Label + " "
	}
	return t + mark + " " + where + e.Message
}

// hr draws a faint rule the width of the screen, used to separate
// unrelated blocks within a tab (the device table from the panes below it).
func hr(th Theme, w int) string {
	if w <= 0 {
		return ""
	}
	return th.border.Render(strings.Repeat("─", w))
}

func sideBySide(left, right string, lw, rw int) string {
	l, r := strings.Split(strings.TrimRight(left, "\n"), "\n"), strings.Split(strings.TrimRight(right, "\n"), "\n")
	var b strings.Builder
	for i := 0; i < max(len(l), len(r)); i++ {
		var a, c string
		if i < len(l) {
			a = l[i]
		}
		if i < len(r) {
			c = r[i]
		}
		b.WriteString(pad(a, lw) + pad(c, rw) + "\n")
	}
	return b.String()
}

func (m Model) viewDevices() string {
	devs := m.devices()
	if len(devs) == 0 {
		return m.empty()
	}
	tableH := min(len(devs), m.tableHeight())
	mm := m
	table := mm.deviceTable(devs, tableH)
	sel := min(m.sel, len(devs)-1)
	detail := strings.Split(m.detail(devs[sel]), "\n")
	if m.detailScroll > 0 {
		start := min(m.detailScroll, max(len(detail)-1, 0))
		detail = append([]string{m.th.dim.Render(fmt.Sprintf("… %d lines above (wheel or PgUp)", start))}, detail[start:]...)
	}
	return table + "\n\n" + strings.Join(detail, "\n")
}

var histMetrics = []struct {
	k     device.Metric
	name  string
	scale func(d device.Device, hist []float64) float64
}{
	{device.Util, "util", func(device.Device, []float64) float64 { return 100 }},
	{device.MemUsed, "memory", func(d device.Device, h []float64) float64 { return d.Metrics.Or(device.MemTotal, peak(h)) }},
	{device.Power, "power", func(d device.Device, h []float64) float64 { return d.Metrics.Or(device.PowerCap, peak(h)) }},
	{device.Temp, "temp", func(device.Device, []float64) float64 { return 100 }},
	{device.ClockCore, "clock", func(d device.Device, h []float64) float64 { return peak(h) }},
}

// procTrendWidth is how wide one inline process sparkline is, plus its space.
const procTrendWidth = 9

// procSpark draws the recent trend of one process metric. An unknown scale
// falls back to the peak of the samples, so a flat line still reads.
func (m Model) procSpark(d device.Device, p device.Process, k device.Metric, scale float64) string {
	vals := m.eng.ProcTrend(d.ID, p.PID, k, procTrendWidth-1)
	if len(vals) == 0 {
		return pad("", procTrendWidth-1) + " "
	}
	if scale <= 0 {
		scale = peak(vals)
	}
	return m.th.role("spark").Render(pad(spark(vals, scale, procTrendWidth-1), procTrendWidth-1)) + " "
}

func (m Model) detail(d device.Device) string {
	th := m.th
	var b strings.Builder
	meta := fmt.Sprintf("%s #%s", d.Vendor, d.Label())
	if d.Bus != "" {
		meta += "  " + d.Bus
	}
	if d.Parent != "" {
		meta += "  partition of " + d.Parent
	}
	if d.Allocated != "" {
		meta += "  held by " + d.Allocated
	}
	if d.Source != "" {
		meta += "  source " + d.Source
	}
	fmt.Fprintf(&b, "%s  %s  %s\n", th.bold.Render(d.Name), th.dim.Render(meta), m.state(d))
	if en, ok := m.snap.Energy[d.ID]; ok {
		line := fmt.Sprintf("%s %.3f kWh since siltide started (%s)", th.dim.Render("energy"), en.KWh, en.Method)
		if en.CO2g > 0 {
			line += fmt.Sprintf(", %.0f g CO2", en.CO2g)
		}
		b.WriteString(line + "\n")
	}
	if len(d.HealthNotes) > 0 {
		fmt.Fprintf(&b, "%s %s: %s\n", th.dim.Render("health"), m.health(d), strings.Join(d.HealthNotes, "; "))
	}
	b.WriteString("\n")
	if h := m.eng.History(); h != nil {
		sparkW := max(m.width-30, 10)
		for _, c := range histMetrics {
			if _, ok := d.Metrics.Get(c.k); !ok {
				continue
			}
			hist := h.Recent(d.ID, c.k, sparkW)
			fmt.Fprintf(&b, "%-8s %s %s\n", c.name, th.role("spark").Render(pad(spark(hist, c.scale(d, hist), sparkW), sparkW)), rpad(th.opt(d.Metrics, c.k), 9))
		}
		b.WriteString("\n")
	}
	keys := d.Metrics.Keys()
	cols := max(min(m.width/38, 3), 1)
	rows := (len(keys) + cols - 1) / cols
	for i := 0; i < rows; i++ {
		var line string
		for c := 0; c < cols; c++ {
			j := i + c*rows
			if j < len(keys) {
				line += pad(th.dim.Render(string(keys[j])), 16) + " " + pad(trunc(metricText(keys[j], d.Metrics[keys[j]]), 19), 19) + " "
			}
		}
		b.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	if len(d.Links) > 0 {
		b.WriteString("\n" + th.dim.Render(fmt.Sprintf("%-6s %-6s %-4s %-18s %10s %10s %8s", "LINK", "STATE", "VER", "PEER", "RX", "TX", "ERRORS")) + "\n")
		for _, l := range d.Links {
			state := th.ok.Render("up")
			if !l.Active {
				state = th.dim.Render("down")
			}
			fmt.Fprintf(&b, "%-6d %-6s %-4s %-18s %10s %10s %8d\n", l.Index, state, l.Version, trunc(l.Peer, 18), rate(l.Rx), rate(l.Tx), l.Errors)
		}
	}
	if len(d.Topology) > 0 {
		var parts []string
		for _, k := range sortedKeys(d.Topology) {
			parts = append(parts, fmt.Sprintf("%d:%s", k, d.Topology[k]))
		}
		fmt.Fprintf(&b, "\n%s %s\n", th.dim.Render("topology"), strings.Join(parts, " "))
	}
	if len(d.Procs) > 0 {
		total := d.Metrics.Or(device.MemTotal, 0)
		// A trend per process needs room; a narrow terminal keeps the numbers.
		trendW := 0
		if m.width >= 120 {
			trendW = procTrendWidth
		}
		head := fmt.Sprintf("%-8s %-10s %-16s %9s %5s %s%5s %s%8s  %s", "PID", "USER", "PROCESS", "MEM", "MEM%", pad("", trendW), "UTIL", pad("", trendW), "RUNTIME", "POD / JOB / COMMAND")
		b.WriteString("\n" + th.dim.Render(head) + "\n")
		procs := append([]device.Process(nil), d.Procs...)
		sort.SliceStable(procs, func(i, j int) bool { return procLess(procs[i], procs[j]) })
		for _, p := range procs {
			where := p.Command
			switch {
			case p.Pod != "":
				where = p.Namespace + "/" + p.Pod + th.dim.Render("  "+p.Command)
			case p.Job != "":
				where = "job " + p.Job + th.dim.Render("  "+p.Command)
			}
			share := th.dim.Render(na)
			if mem, ok := p.Metrics.Get(device.MemUsed); ok && total > 0 {
				share = fmt.Sprintf("%.0f%%", mem/total*100)
			}
			run := th.dim.Render(na)
			if !p.Started.IsZero() {
				run = age(p.Started)
			}
			memTrend, utilTrend := "", ""
			if trendW > 0 {
				memTrend = m.procSpark(d, p, device.MemUsed, total)
				utilTrend = m.procSpark(d, p, device.Util, 100)
			}
			// Each trend follows the numbers it belongs to: memory, its
			// trend, then utilization and its own.
			fmt.Fprintf(&b, "%-8d %-10s %-16s %s %s %s%s %s%s  %s\n", p.PID, trunc(orQ(p.User), 10), trunc(orQ(p.Name), 16),
				rpad(th.opt(p.Metrics, device.MemUsed), 9), rpad(share, 5), memTrend,
				rpad(th.opt(p.Metrics, device.Util), 5), utilTrend, rpad(run, 8),
				trunc(where, max(m.width-72-2*trendW, 10)))
			if len(p.App) > 0 {
				var parts []string
				for _, k := range sortedStrKeys(p.App) {
					parts = append(parts, fmt.Sprintf("%s %s", k, fmtNum(p.App[k])))
				}
				fmt.Fprintf(&b, "         %s %s\n", th.info.Render("app"), strings.Join(parts, "  "))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sortedKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func sortedStrKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fmtNum(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e9 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// age formats how long ago t was, coarse.
func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d > 48*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d > time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

type procRow struct {
	d device.Device
	p device.Process
}

func procColumns(w int) []column {
	return []column{{"PID", 7, true}, {"USER", 10, false}, {"PROCESS", 16, false}, {"DEV", 8, false}, {"MEM", 9, true}, {"UTIL", 5, true},
		{"NAMESPACE/POD", 32, false}, {"CONTAINER", 12, false}, {"WORKLOAD", 24, false}, {"COMMAND", max(w-140, 10), false}}
}

// procSortKeys follow procColumns.
var procSortKeys = []func(procRow) string{
	func(r procRow) string { return fmt.Sprintf("%012d", r.p.PID) },
	func(r procRow) string { return r.p.User },
	func(r procRow) string { return r.p.Name },
	func(r procRow) string { return r.d.Node + fmt.Sprintf("%06d", r.d.Index) },
	func(r procRow) string { return fmt.Sprintf("%020.0f", r.p.Metrics.Or(device.MemUsed, -1)) },
	func(r procRow) string { return fmt.Sprintf("%06.1f", r.p.Metrics.Or(device.Util, -1)) },
	func(r procRow) string { return r.p.Namespace + "/" + r.p.Pod },
	func(r procRow) string { return r.p.Container },
	func(r procRow) string { return r.p.Workload },
	func(r procRow) string { return r.p.Command },
}

// processes lists every process in view, sorted and filtered.
func (m Model) processes() []procRow {
	var rows []procRow
	for _, d := range m.devices() {
		for _, p := range d.Procs {
			if !m.filter.Empty() && !m.filter.Process(d, p) {
				continue
			}
			if m.ns != "" && p.Namespace != m.ns {
				continue
			}
			rows = append(rows, procRow{d, p})
		}
	}
	col := m.sortCol
	if m.tab != tabProcesses {
		col = 4
	}
	key := procSortKeys[min(col, len(procSortKeys)-1)]
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := key(rows[i]), key(rows[j])
		if m.sortDesc {
			return a > b
		}
		return a < b
	})
	return rows
}

func (m *Model) viewProcesses() string {
	th := m.th
	rows := m.processes()
	if len(rows) == 0 {
		return "no processes" + map[bool]string{true: " match " + fmt.Sprintf("%q", m.search), false: ""}[m.search != ""]
	}
	cols := procColumns(m.width)
	var out [][]string
	for _, r := range rows {
		pod := ""
		if r.p.Pod != "" {
			pod = r.p.Namespace + "/" + r.p.Pod
		} else if r.p.Job != "" {
			pod = "job " + r.p.Job
		}
		out = append(out, []string{fmt.Sprint(r.p.PID), orQ(r.p.User), orQ(r.p.Name), r.d.Label(), th.opt(r.p.Metrics, device.MemUsed), th.opt(r.p.Metrics, device.Util), pod, r.p.Container, r.p.Workload, r.p.Command})
	}
	m.headSpans = headerSpans(cols)
	dir := "desc"
	if !m.sortDesc {
		dir = "asc"
	}
	return th.dim.Render(fmt.Sprintf("%d processes · sorted by %s %s · %ss next column, S reverse, x marks a row",
		len(rows), strings.ToLower(cols[min(m.sortCol, len(cols)-1)].name), dir, m.markNote())) + "\n" +
		th.tableMarks(cols, out, m.sel, m.height-6, m.width, m.sortCol, m.sortDesc, m.markedRows())
}

// metricTab renders a bar per device for one metric family.
func (m Model) metricTab(title string, cols []column, row func(d device.Device) []string, footer string) string {
	devs := m.devices()
	if len(devs) == 0 {
		return m.empty()
	}
	var rows [][]string
	for _, d := range devs {
		rows = append(rows, row(d))
	}
	out := m.th.bold.Render(title) + "\n" + m.th.table(cols, rows, m.sel, m.height-8, m.width, -1, false)
	if footer != "" {
		out += "\n\n" + footer
	}
	return out
}

func (m Model) viewMemory() string {
	th := m.th
	barW := min(max((m.width-95)/2, 8), 30)
	cols := []column{{"#", 6, false}, {"NAME", 24, false}, {"USED", barW + 5, false}, {"", 13, false}, {"FREE", 8, true}, {"BANDWIDTH", 10, true}, {"ECC", 8, true}, {"REMAP", 6, true}, {"PROCS", 5, true}, {"TOP PROCESS", 30, false}}
	f := m.snap.Fleet
	footer := ""
	if f.MemTotal > 0 {
		footer = fmt.Sprintf("%s %s of %s in use across the fleet (%.0f%%)", th.dim.Render("total"), bytes(f.MemUsed), bytes(f.MemTotal), f.MemUsed/f.MemTotal*100)
	}
	return m.metricTab("Memory", cols, func(d device.Device) []string {
		pct, ok := d.MemPercent()
		free := th.dim.Render(na)
		if used, ok1 := d.Metrics.Get(device.MemUsed); ok1 {
			if total, ok2 := d.Metrics.Get(device.MemTotal); ok2 {
				free = bytes(total - used)
			}
		}
		top := ""
		if p := topProc(d); p != nil {
			top = fmt.Sprintf("%s %s", orQ(p.Name), th.dim.Render(bytes(p.Metrics.Or(device.MemUsed, 0))))
		}
		ecc := th.dim.Render(na)
		if c, ok := d.Metrics.Get(device.EccCorrected); ok {
			ecc = fmt.Sprintf("%.0f/%.0f", c, d.Metrics.Or(device.EccUncorrected, 0))
			if d.Metrics.Or(device.EccUncorrected, 0) > 0 {
				ecc = th.warn.Render(ecc)
			}
		}
		remap := th.dim.Render(na)
		if r, ok := d.Metrics.Get(device.RemappedRows); ok {
			remap = fmt.Sprintf("%.0f", r)
			if d.Metrics.Or(device.RemapPending, 0) > 0 || d.Metrics.Or(device.RemapFailed, 0) > 0 {
				remap = th.warn.Render(remap + "!")
			}
		}
		return []string{d.Label(), d.Name, th.pctBar(pct, ok, barW), memText(d.Metrics), free, th.opt(d.Metrics, device.MemBandwidth), ecc, remap, fmt.Sprint(len(d.Procs)), top}
	}, footer)
}

func topProc(d device.Device) *device.Process {
	var top *device.Process
	for i := range d.Procs {
		if top == nil || d.Procs[i].Metrics.Or(device.MemUsed, 0) > top.Metrics.Or(device.MemUsed, 0) {
			top = &d.Procs[i]
		}
	}
	return top
}

func (m Model) viewPower() string {
	th := m.th
	barW := min(max((m.width-110)/2, 8), 30)
	cols := []column{{"#", 6, false}, {"NAME", 24, false}, {"POWER", barW + 5, false}, {"", 12, false}, {"ENERGY", 9, true}, {"CORE", 8, true}, {"MEMCLK", 8, true}, {"PSTATE", 6, true}, {"FAN", 5, true}, {"THROTTLE", 26, false}, {"VIOLATION", 12, false}}
	f := m.snap.Fleet
	footer := ""
	if f.PowerW > 0 {
		footer = fmt.Sprintf("%s %.0fW", th.dim.Render("fleet"), f.PowerW)
		if f.PowerCapW > 0 {
			footer += fmt.Sprintf(" of %.0fW cap (%.0f%%)", f.PowerCapW, f.PowerW/f.PowerCapW*100)
		}
	}
	return m.metricTab("Power and clocks", cols, func(d device.Device) []string {
		p, ok := d.Metrics.Get(device.Power)
		capW, okc := d.Metrics.Get(device.PowerCap)
		pct := -1.0
		if ok && okc && capW > 0 {
			pct = p / capW * 100
		}
		text := th.opt(d.Metrics, device.Power)
		if okc {
			text += th.dim.Render(fmt.Sprintf("/%.0fW", capW))
		}
		thr := th.dim.Render(na)
		if v, ok := d.Metrics.Get(device.Throttle); ok {
			thr = metricText(device.Throttle, v)
			if int(v)&^device.ThrottleIdle != 0 {
				thr = th.warn.Render(thr)
			}
		}
		energy := th.dim.Render(na)
		if e, ok := d.Metrics.Get(device.Energy); ok {
			energy = fmt.Sprintf("%.1fkWh", e/3.6e6)
		}
		pstate := th.dim.Render(na)
		if p, ok := d.Metrics.Get(device.PState); ok {
			pstate = fmt.Sprintf("P%.0f", p)
		}
		viol := th.dim.Render(na)
		if p, ok := d.Metrics.Get(device.ViolationPower); ok {
			viol = fmt.Sprintf("pwr %.0f%% th %.0f%%", p, d.Metrics.Or(device.ViolationTherm, 0))
		}
		return []string{d.Label(), d.Name, th.pctBar(pct, pct >= 0, barW), text, energy, th.opt(d.Metrics, device.ClockCore), th.opt(d.Metrics, device.ClockMem), pstate, th.opt(d.Metrics, device.Fan), thr, viol}
	}, footer)
}

func (m Model) viewThermals() string {
	th := m.th
	barW := min(max((m.width-90)/2, 8), 30)
	cols := []column{{"#", 6, false}, {"NAME", 24, false}, {"TEMP", barW + 5, false}, {"", 6, true}, {"MEMTEMP", 7, true}, {"SLOWDOWN", 8, true}, {"SHUTDOWN", 8, true}, {"HEADROOM", 8, true}, {"FAN", 5, true}, {"THROTTLE", 10, false}, {"HEALTH", 6, true}}
	return m.metricTab("Thermals", cols, func(d device.Device) []string {
		t, ok := d.Metrics.Get(device.Temp)
		state := th.dim.Render("no")
		if int(d.Metrics.Or(device.Throttle, 0))&device.ThrottleThermal != 0 {
			state = th.warn.Render("thermal")
		}
		text := th.opt(d.Metrics, device.Temp)
		if ok && t >= m.tempWarn {
			text = th.warn.Render(text)
		}
		headroom := th.dim.Render(na)
		limit, okl := d.Metrics.Get(device.TempSlowdown)
		if !okl {
			limit, okl = m.tempWarn, ok
		}
		if ok && okl {
			headroom = fmt.Sprintf("%.0f°C", limit-t)
			if limit-t < 10 {
				headroom = th.warn.Render(headroom)
			}
		}
		return []string{d.Label(), d.Name, th.pctBar(t, ok, barW), text, th.opt(d.Metrics, device.MemTemp), th.opt(d.Metrics, device.TempSlowdown), th.opt(d.Metrics, device.TempShutdown), headroom, th.opt(d.Metrics, device.Fan), state, m.health(d)}
	}, th.dim.Render(fmt.Sprintf("bars are 0-100°C; warning at %.0f°C", m.tempWarn)))
}

func (m Model) viewLinks() string {
	th := m.th
	cols := []column{{"#", 6, false}, {"NAME", 20, false}, {"NUMA", 4, true}, {"PCIE", 11, false}, {"MAX", 11, false}, {"RX", 9, true}, {"TX", 9, true}, {"REPLAYS", 7, true}, {"LINKS", 7, true}, {"LINK RX", 9, true}, {"LINK TX", 9, true}, {"LINK ERR", 8, true}, {"PART", 4, true}, {"NOTE", 24, false}}
	return m.metricTab("PCIe, interconnects, and partitions", cols, func(d device.Device) []string {
		pcie := th.dim.Render(na)
		if g, ok := d.Metrics.Get(device.PCIeGen); ok {
			pcie = fmt.Sprintf("gen%.0f x%.0f", g, d.Metrics.Or(device.PCIeWidth, 0))
		}
		maxP := th.dim.Render(na)
		if g, ok := d.Metrics.Get(device.PCIeMaxGen); ok {
			maxP = fmt.Sprintf("gen%.0f x%.0f", g, d.Metrics.Or(device.PCIeMaxWidth, 0))
		}
		links := th.dim.Render(na)
		var rx, tx float64
		var errs uint64
		for _, l := range d.Links {
			rx, tx, errs = rx+l.Rx, tx+l.Tx, errs+l.Errors
		}
		if t, ok := d.Metrics.Get(device.LinksTotal); ok {
			a := d.Metrics.Or(device.LinksActive, 0)
			links = fmt.Sprintf("%.0f/%.0f", a, t)
			if a < t {
				links = th.warn.Render(links)
			}
		}
		lrx, ltx, lerr := th.dim.Render(na), th.dim.Render(na), th.dim.Render(na)
		if len(d.Links) > 0 {
			lrx, ltx, lerr = rate(rx), rate(tx), fmt.Sprint(errs)
			if errs > 0 {
				lerr = th.warn.Render(lerr)
			}
		}
		note := ""
		for _, n := range d.HealthNotes {
			if strings.Contains(n, "PCIe") {
				note = th.warn.Render(n)
			}
		}
		return []string{d.Label(), d.Name, th.opt(d.Metrics, device.NUMANode), pcie, maxP, th.opt(d.Metrics, device.PCIeRx), th.opt(d.Metrics, device.PCIeTx), th.opt(d.Metrics, device.PCIeReplays), links, lrx, ltx, lerr, th.opt(d.Metrics, device.Partitions), note}
	}, strings.TrimRight(m.topologyMatrix(m.devices()), "\n"))
}

// histWindow is the visible time range of the history and dashboard tabs.
func (m Model) histWindow() (time.Time, time.Time) {
	h := m.eng.History()
	if h == nil {
		return time.Time{}, time.Time{}
	}
	_, hi := h.Span()
	end := hi
	if !m.cursor.IsZero() {
		end = m.cursor.Add(m.window / 4)
		if end.After(hi) {
			end = hi
		}
	}
	return end.Add(-m.window), end
}

// seriesIn returns a device's metric within the window, resampled to width.
func (m Model) seriesIn(id string, k device.Metric, from, to time.Time, width int) []float64 {
	h := m.eng.History()
	pts := h.Between(id, from, to)
	vals := make([]float64, len(pts))
	for i, p := range pts {
		v, ok := p.Get(k)
		if !ok {
			v = math.NaN()
		}
		vals[i] = v
	}
	return resample(vals, width)
}

func (m Model) viewHistory() string {
	th := m.th
	h := m.eng.History()
	if h == nil {
		return "history is disabled"
	}
	lo, hi := h.Span()
	if hi.IsZero() {
		return "no history yet"
	}
	hm := histMetrics[m.histMetric]
	cursor := m.cursor
	live := cursor.IsZero()
	if live {
		cursor = hi
	}
	from, to := m.histWindow()
	at := h.At(cursor)
	sparkW := max(m.width-50, 10)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s   %s %s   %s %s → %s   %s %s   %s\n", th.dim.Render("metric"), th.accent.Render(hm.name),
		th.dim.Render("window"), m.window, th.dim.Render("showing"), from.Format("15:04:05"), to.Format("15:04:05"),
		th.dim.Render("cursor"), map[bool]string{true: th.ok.Render("live"), false: th.mid.Render(cursor.Format("2006-01-02 15:04:05"))}[live],
		th.dim.Render(fmt.Sprintf("(history since %s)", lo.Format("15:04"))))
	b.WriteString("\n")
	pos := sparkW - 1
	if span := to.Sub(from); span > 0 {
		pos = int(float64(sparkW-1) * float64(cursor.Sub(from)) / float64(span))
	}
	devs := m.devices()
	rows := 0
	lw := labelWidth(devs)
	for i, d := range devs {
		if rows >= m.height-12 {
			break
		}
		if m.pick >= 0 && i != m.pick {
			continue
		}
		vals := m.seriesIn(d.ID, hm.k, from, to, sparkW)
		if len(vals) == 0 {
			continue
		}
		line := []rune(pad(spark(vals, hm.scale(d, vals), sparkW), sparkW))
		if pos >= 0 && pos < len(line) {
			line[pos] = '│'
		}
		val := th.dim.Render(na)
		if p, ok := at[d.ID]; ok {
			if v, ok := p.Get(hm.k); ok {
				val = metricText(hm.k, v)
			}
		}
		fmt.Fprintf(&b, "%-*s %-14s %s %s\n", lw, trunc(d.Label(), lw), trunc(d.Name, 14), th.role("spark").Render(string(line)), rpad(val, 9))
		rows++
	}
	pick := m.pick
	if pick < 0 {
		pick = min(m.sel, len(devs)-1)
	}
	if pick >= 0 && pick < len(devs) {
		d := devs[pick]
		b.WriteString("\n" + th.bold.Render("Inspector") + th.dim.Render("  "+d.Label()+" "+d.Name+" at cursor  ({ } picks a device)") + "\n")
		if p, ok := at[d.ID]; ok {
			var parts []string
			for _, hm := range histMetrics {
				if v, ok := p.Get(hm.k); ok {
					parts = append(parts, fmt.Sprintf("%s %s", th.dim.Render(hm.name), metricText(hm.k, v)))
				}
			}
			b.WriteString(strings.Join(parts, "   ") + "\n")
		} else {
			b.WriteString(th.dim.Render("no sample at the cursor") + "\n")
		}
	}
	b.WriteString("\n" + th.bold.Render("Events up to the cursor") + "\n")
	evs := m.eng.Events()
	shown := 0
	for i := len(evs) - 1; i >= 0 && shown < 4; i-- {
		if evs[i].Time.After(cursor) {
			continue
		}
		b.WriteString(trunc(m.eventLine(evs[i], true), m.width) + "\n")
		shown++
	}
	if shown == 0 {
		b.WriteString(th.dim.Render("none") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) eventRows() []events.Event {
	evs := m.eng.Events()
	var out []events.Event
	for i := len(evs) - 1; i >= 0; i-- {
		if !m.filter.Empty() && !m.filter.Event(evs[i]) {
			continue
		}
		out = append(out, evs[i])
	}
	return out
}

func (m *Model) viewEvents() string {
	th := m.th
	var b strings.Builder
	b.WriteString(th.bold.Render("Active alerts") + "\n")
	if len(m.snap.Alerts) == 0 {
		b.WriteString(th.dim.Render("none") + "\n")
	}
	for _, a := range m.snap.Alerts {
		b.WriteString(trunc(m.eventLine(a, true), m.width) + "\n")
	}
	evs := m.eventRows()
	b.WriteString("\n" + th.bold.Render("Events") + th.dim.Render(fmt.Sprintf(" %d, newest first · filter with sev:critical kind:xid dev:3 !sev:info", len(evs))) + "\n")
	if tally := m.kindTally(evs); tally != "" {
		b.WriteString(tally + "\n")
	}
	var rows [][]string
	for _, e := range evs {
		sev := string(e.Severity)
		switch e.Severity {
		case events.Warning:
			sev = th.warn.Render(sev)
		case events.Critical:
			sev = th.crit.Render(sev)
		}
		rows = append(rows, []string{e.Time.Format("15:04:05"), sev, e.Label, e.Kind, e.Message})
	}
	cols := []column{{"TIME", 8, false}, {"SEV", 8, false}, {"DEV", 8, false}, {"KIND", 16, false}, {"MESSAGE", max(m.width-46, 20), false}}
	m.headSpans = nil
	b.WriteString(th.table(cols, rows, m.sel, m.height-9-len(m.snap.Alerts), m.width, -1, false))
	return b.String()
}

// kindTally summarises what the log is made of: each kind with how many
// events carry it, most frequent first. On a fleet having a bad morning the
// same three kinds are most of the list, and reading two hundred rows to
// learn that is the slowest way to find out.
func (m Model) kindTally(evs []events.Event) string {
	if len(evs) < 2 {
		return ""
	}
	counts, worst := map[string]int{}, map[string]events.Severity{}
	for _, e := range evs {
		counts[e.Kind]++
		if rank(e.Severity) > rank(worst[e.Kind]) {
			worst[e.Kind] = e.Severity
		}
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool {
		if counts[kinds[i]] != counts[kinds[j]] {
			return counts[kinds[i]] > counts[kinds[j]]
		}
		return kinds[i] < kinds[j]
	})

	th := m.th
	var parts []string
	for _, k := range kinds {
		part := fmt.Sprintf("%d %s", counts[k], k)
		switch worst[k] {
		case events.Critical:
			part = th.crit.Render(part)
		case events.Warning:
			part = th.warn.Render(part)
		default:
			part = th.dim.Render(part)
		}
		parts = append(parts, part)
	}
	return trunc(th.dim.Render("by kind ")+strings.Join(parts, th.dim.Render(" · ")), m.width)
}

// rank orders severities so a kind is coloured by the worst event in it.
func rank(s events.Severity) int {
	switch s {
	case events.Critical:
		return 3
	case events.Warning:
		return 2
	case events.Info:
		return 1
	}
	return 0
}

func (m Model) viewHelp() string {
	th := m.th
	var b strings.Builder
	b.WriteString(th.bold.Render("Keys") + "\n")
	for _, k := range []struct {
		a    Action
		desc string
	}{
		{ActUp, "select previous row"}, {ActDown, "select next row"}, {ActPageUp, "up 10 rows"}, {ActPageDown, "down 10 rows"},
		{ActEnter, "open the selected row (device detail, pod describe)"}, {ActBack, "clear filters, then back to the overview"},
		{ActNextTab, "next tab"}, {ActPrevTab, "previous tab"}, {ActPause, "pause updates"}, {ActRefresh, "collect now"},
		{ActSearch, "filter: words, dev:3 user:vllm ns:ml, util>80 temp>=70, ! negates"}, {ActCommand, "command bar: it lists what it takes, ↑↓ to move, Tab to take one"},
		{ActSortNext, "sort by the next column"}, {ActSortRev, "reverse the sort"},
		{ActHistBack, "history: one step back"}, {ActHistFwd, "history: one step forward"}, {ActHistJump, "history: 30 steps"},
		{ActHistLive, "history: back to now"}, {ActMetric, "history: next metric"}, {ActZoomIn, "history: narrower window"}, {ActZoomOut, "history: wider window"},
		{ActPrevDev, "history: previous device"}, {ActNextDev, "history: next device"},
		{ActDescribe, "Kubernetes: describe pod"}, {ActLogs, "Kubernetes: container logs"}, {ActNextCont, "logs: next container"}, {ActWrap, "logs: wrap lines"},
		{ActMark, "mark the row, or unmark it (devices, processes, pods)"}, {ActMarkAll, "mark every row in view, or clear them all"},
		{ActYank, "copy the marked ids: pids, pod names, device ids"}, {ActYankCmd, "copy the command for them: kill, kubectl delete pod. siltide does not run it"},
		{ActDash, "dashboard"}, {ActNextNode, "next node (filters every tab)"}, {ActPrevNode, "previous node"}, {ActExport, "export history of the devices in view to CSV"},
		{ActHelp, "this screen"}, {ActQuit, "quit"},
	} {
		fmt.Fprintf(&b, "  %-14s %s\n", m.keys.Keys(k.a), th.dim.Render(k.desc))
	}
	b.WriteString("  " + pad("1-9 0 N K W D H", 14) + " " + th.dim.Render("switch tab; tabs, rows, column headers, and footer hints are clickable, the wheel scrolls, double-click opens") + "\n")
	b.WriteString("\n" + th.bold.Render("Commands") + th.dim.Render("  (: then)") + "\n")
	for _, c := range [][2]string{
		{"<tab name>", "switch tab, e.g. :proc"}, {"sort <column>", "sort the current table"}, {"filter <text>", "same as /"},
		{"node <name>|local", "show one node only; :node clears"}, {"ns <namespace>", "pods of one namespace"}, {"<pod name>", "jump to a pod"},
		{"describe | logs", "for the selected pod"}, {"metric util|memory|power|temp|clock", "history metric"}, {"window 15m|1h|24h", "history window"},
		{"compare 3 5 | compare 3 15:04", "2 devices, or one device now and then"},
		{"theme <name>", "built in: " + strings.Join(ThemeNames(), ", ") + ", or a file in ~/.config/siltide/themes"},
		{"live", "leave the time machine"}, {"pause", "toggle updates"}, {"refresh", "collect now"},
	} {
		fmt.Fprintf(&b, "  %-38s %s\n", c[0], th.dim.Render(c[1]))
	}
	b.WriteString("\n" + th.bold.Render("Vendors") + th.dim.Render("  every provider is probed at start; absent ones cost one lookup") + "\n")
	for _, p := range m.snap.Providers {
		state := th.ok.Render("active")
		if !p.Active {
			state = th.dim.Render("absent")
		} else if p.Error != "" {
			state = th.warn.Render("error")
		}
		fmt.Fprintf(&b, "  %-56s %s\n", trunc(p.Label, 56), state)
		if p.Error != "" {
			fmt.Fprintf(&b, "  %-56s %s\n", "", th.dim.Render(trunc(p.Error, m.width-60)))
		}
		if !p.Active && p.Hint != "" {
			fmt.Fprintf(&b, "  %-56s %s\n", "", th.dim.Render(trunc(p.Hint, m.width-60)))
		}
	}
	b.WriteString("\n" + th.dim.Render("Health scores, states, outliers, idle-allocated flags, and costs are derived by siltide, not reported by vendors."))
	return b.String()
}

// Plain renders the overview for `--once` without a selection.
func Plain(s collect.Snapshot, th Theme, w int) string {
	m := Model{snap: s, th: th, width: w, height: 1000, sel: -1, sortCol: -1, sels: map[int]int{}, currency: "$"}
	head := fmt.Sprintf("siltide  %s  %d devices  %s", s.Host, len(s.Devices), s.Time.Format(time.DateTime))
	if s.Demo {
		head += "  [DEMO: simulated data]"
	}
	f := s.Fleet
	line := fmt.Sprintf("busy %d  active %d  idle %d  down %d  allocated %d", f.Busy, f.Active, f.Idle, f.Down, f.Allocated)
	if f.PowerW > 0 {
		line += fmt.Sprintf("  power %.0fW", f.PowerW)
	}
	if f.MemTotal > 0 {
		line += fmt.Sprintf("  memory %s/%s", bytes(f.MemUsed), bytes(f.MemTotal))
	}
	var b strings.Builder
	b.WriteString(head + "\n" + line + "\n\n")
	if len(s.Devices) == 0 {
		b.WriteString(m.empty() + "\n")
		return b.String()
	}
	b.WriteString(m.deviceTable(s.Devices, 0) + "\n")
	for _, a := range s.Alerts {
		b.WriteString("\n" + strings.ToUpper(string(a.Severity)) + " #" + a.Label + ": " + a.Message)
	}
	if len(s.Alerts) > 0 {
		b.WriteString("\n")
	}
	return b.String()
}
