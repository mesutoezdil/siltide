package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/history"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/provider/sim"
)

func demoEngine(t *testing.T) *collect.Engine {
	t.Helper()
	hist, _ := history.Open(history.Options{Keep: time.Hour, Resolution: time.Millisecond})
	e := collect.New([]provider.Provider{sim.Provider(8)}, config.Default(), hist, true)
	e.Detect()
	e.Collect(context.Background())
	time.Sleep(2 * time.Millisecond)
	e.Collect(context.Background())
	return e
}

func TestEveryTabRenders(t *testing.T) {
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("default", nil)})
	m.width, m.height = 160, 50
	for i := range tabs {
		m.setTab(i)
		out := m.View()
		if strings.Contains(out, "%!") || strings.Count(out, "\n") < 3 {
			t.Errorf("tab %s rendered badly:\n%s", tabs[i].name, out)
		}
		if width(strings.Split(out, "\n")[0]) > 160 {
			t.Errorf("tab %s header wider than the screen", tabs[i].name)
		}
	}
	// narrow terminal must not panic
	m.width, m.height = 60, 15
	for i := range tabs {
		m.setTab(i)
		_ = m.View()
	}
}

func TestKeysAndCommands(t *testing.T) {
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("mono", nil), Keys: NewKeymap(map[string]string{"quit": "x"})})
	press := func(k string) {
		var msg tea.Msg
		if len(k) == 1 {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		} else {
			msg = tea.KeyMsg{Type: map[string]tea.KeyType{"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "down": tea.KeyDown}[k]}
		}
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	press("down")
	press("down")
	press("enter")
	if m.tab != tabDevices || m.sel != 2 {
		t.Fatalf("tab %d sel %d", m.tab, m.sel)
	}
	press(":")
	for _, c := range "proc" {
		press(string(c))
	}
	press("enter")
	if m.tab != tabProcesses {
		t.Fatalf("command :proc gave tab %d", m.tab)
	}
	press("/")
	for _, c := range "vllm" {
		press(string(c))
	}
	press("enter")
	for _, r := range m.processes() {
		if r.p.Name != "vllm" {
			t.Fatalf("filter leaked %s", r.p.Name)
		}
	}
	press(":")
	for _, c := range "theme nope" {
		press(string(c))
	}
	press("enter")
	if !strings.Contains(m.notice, "built in:") {
		t.Fatalf("notice %q", m.notice)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd == nil {
		t.Fatal("rebound quit key did nothing")
	}
}

func TestHistoryScrub(t *testing.T) {
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("default", nil)})
	m.setTab(tabHistory)
	m.scrub(-1)
	if m.cursor.IsZero() {
		t.Fatal("cursor should move off live")
	}
	if !strings.Contains(m.View(), "TIME MACHINE") {
		t.Fatal("header must show the time machine")
	}
	m.scrub(100)
	if !m.cursor.IsZero() {
		t.Fatal("scrubbing past the end returns to live")
	}
}

func TestPlain(t *testing.T) {
	e := demoEngine(t)
	out := Plain(e.Snapshot(), NewTheme("mono", nil), 120)
	if !strings.Contains(out, "DEMO") || !strings.Contains(out, "H100") || !strings.Contains(out, "WARNING") {
		t.Fatalf("plain:\n%s", out)
	}
}

func TestFilterLanguage(t *testing.T) {
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("default", nil)})
	m.setSearch("ns:inference")
	for _, d := range m.devices() {
		hit := false
		for _, p := range d.Procs {
			if p.Namespace == "inference" {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("device %s passed ns:inference without a matching process", d.ID)
		}
	}
	m.setSearch("dev:3 vendor:nvidia")
	if devs := m.devices(); len(devs) != 1 || devs[0].Index != 3 {
		t.Fatalf("dev:3 -> %d devices", len(devs))
	}
	m.setTab(tabEvents)
	m.setSearch("sev:warning")
	for _, ev := range m.eventRows() {
		if ev.Severity != "warning" {
			t.Fatalf("sev filter leaked %s", ev.Severity)
		}
	}
}

func TestOverlaysAndTabs(t *testing.T) {
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("solarized", nil)})
	m.width, m.height = 140, 40
	m.setTab(tabKube)
	if len(m.pods()) == 0 {
		t.Fatal("demo must have pods")
	}
	m.describe()
	if m.overlay != overlayDescribe || !strings.Contains(m.View(), "Accelerators:") {
		t.Fatalf("describe overlay: %d", m.overlay)
	}
	m.logs()
	if m.overlay != overlayLogs {
		t.Fatal("logs overlay")
	}
	mm := m.overlayKey("esc")
	if mm.overlay != overlayNone {
		t.Fatal("esc closes the overlay")
	}
	m.startCompare([]string{"0", "1"})
	if !strings.Contains(m.View(), "Compare") {
		t.Fatal("compare view")
	}
	m.overlay = overlayNone
	for _, tab := range []int{tabNetwork, tabDashboard, tabHealth, tabNodes, tabWorkloads} {
		m.setTab(tab)
		if out := m.View(); strings.Contains(out, "%!") || len(out) < 50 {
			t.Fatalf("tab %d: %q", tab, out)
		}
	}
	m.setTab(tabOverview)
	m.sortBy(3)
	devs := m.devices()
	if len(devs) > 1 && devs[0].Metrics.Or("util", 0) < devs[1].Metrics.Or("util", 0) {
		t.Fatal("sort by util desc")
	}
}

func TestNodeCycleAndExport(t *testing.T) {
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("default", nil)})
	m.cycleNode(1)
	if m.node != "local" {
		t.Fatalf("node %q", m.node)
	}
	m.cycleNode(1)
	if m.node != "ascend-01" { // the demo fleet spans several nodes
		t.Fatalf("node %q", m.node)
	}
	m.cycleNode(-2)
	if m.node != "" {
		t.Fatalf("node %q", m.node)
	}
	var b strings.Builder
	n, err := ExportCSV(&b, e.History(), m.devices())
	if err != nil || n == 0 || !strings.HasPrefix(b.String(), "time,device,name,metric,value") {
		t.Fatalf("%d %v %q", n, err, b.String()[:min(40, len(b.String()))])
	}
	m.width, m.height = 160, 50
	m.setTab(tabLinks)
	if out := m.View(); !strings.Contains(out, "Topology") || !strings.Contains(out, "NV18") {
		t.Fatalf("links tab lacks the topology matrix")
	}
}

func TestReloadCommand(t *testing.T) {
	e := demoEngine(t)
	calls := 0
	var reload func() (Options, error)
	reload = func() (Options, error) {
		calls++
		if calls == 2 {
			return Options{}, errors.New("config.yaml: line 3: unknown key")
		}
		return Options{Theme: NewTheme("nord", nil), Keys: NewKeymap(map[string]string{"quit": "x"}), TempWarn: 70, Currency: "€", Reload: reload}, nil
	}
	m := New(e, Options{Theme: NewTheme("default", nil), Reload: reload})

	m.command("reload")
	if calls != 1 {
		t.Fatalf("reload ran %d times, want 1", calls)
	}
	if m.th.Name != "nord" || m.tempWarn != 70 || m.currency != "€" {
		t.Fatalf("reload did not apply: theme %s tempWarn %v currency %s", m.th.Name, m.tempWarn, m.currency)
	}
	if m.keys["x"] != ActQuit {
		t.Error("reload did not apply the new key bindings")
	}
	if !strings.Contains(m.notice, "reloaded") {
		t.Errorf("notice %q", m.notice)
	}

	m.command("reload") // the file no longer parses
	if m.th.Name != "nord" || m.tempWarn != 70 {
		t.Error("a failed reload must leave the running settings alone")
	}
	if !strings.Contains(m.notice, "unknown key") {
		t.Errorf("notice %q", m.notice)
	}

	m.reload = nil
	m.command("reload")
	if !strings.Contains(m.notice, "only available") {
		t.Errorf("notice %q", m.notice)
	}
}

func TestDetailProcessTrend(t *testing.T) {
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("mono", nil)})
	m.snap = e.Snapshot()
	var dev device.Device
	for _, d := range m.snap.Devices {
		if len(d.Procs) > 0 {
			dev = d
			break
		}
	}
	if dev.ID == "" {
		t.Skip("the demo fleet reported no processes")
	}

	m.width, m.height = 160, 50
	wide := m.detail(dev)
	line := procLine(t, wide, dev.Procs[0].PID)
	if !strings.ContainsAny(line, string(sparks)) {
		t.Errorf("no trend on a wide process row: %q", line)
	}

	m.width = 100
	narrow := procLine(t, m.detail(dev), dev.Procs[0].PID)
	if strings.ContainsAny(narrow, string(sparks)) {
		t.Errorf("a narrow terminal should keep the numbers only: %q", narrow)
	}
	if len(narrow) >= len(line) {
		t.Errorf("the narrow row (%d) should be shorter than the wide one (%d)", len(narrow), len(line))
	}
}

// procLine finds the process row for pid in a rendered detail view.
func procLine(t *testing.T, view string, pid int) string {
	t.Helper()
	for _, l := range strings.Split(view, "\n") {
		if strings.HasPrefix(l, fmt.Sprint(pid)+" ") {
			return l
		}
	}
	t.Fatalf("no row for pid %d in:\n%s", pid, view)
	return ""
}

func TestSelfLogView(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "siltide.log")
	if err := os.WriteFile(path, []byte("2026/09/18 starting\n2026/09/18 nvml: no library\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := demoEngine(t)

	// without a log file the command says how to turn one on
	m := New(e, Options{Theme: NewTheme("mono", nil)})
	m.command("log")
	if m.overlay != overlayNone || !strings.Contains(m.notice, "--debug") {
		t.Fatalf("overlay %d notice %q", m.overlay, m.notice)
	}

	m = New(e, Options{Theme: NewTheme("mono", nil), LogFile: path})
	m.width, m.height = 120, 30
	m.command("log")
	if m.overlay != overlaySelfLog {
		t.Fatalf("overlay %d, want the log view", m.overlay)
	}
	if !strings.Contains(m.text, "nvml: no library") || !strings.Contains(m.textTitle, path) {
		t.Fatalf("text %q title %q", m.text, m.textTitle)
	}
	if !strings.Contains(m.View(), "nvml: no library") {
		t.Error("the log is not on screen")
	}

	// r re-reads, so a failure that happens while the view is open shows up
	if err := os.WriteFile(path, []byte("2026/09/18 starting\n2026/09/18 nvml: no library\n2026/09/18 amd: sysfs gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = m.overlayKey("r")
	if !strings.Contains(m.text, "amd: sysfs gone") {
		t.Error("r did not re-read the file")
	}
	m = m.overlayKey("esc")
	if m.overlay != overlayNone {
		t.Errorf("esc left overlay %d", m.overlay)
	}

	// a file that is there but empty says so rather than showing nothing
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m.command("log")
	if !strings.Contains(m.text, "empty") {
		t.Errorf("text %q", m.text)
	}
}
