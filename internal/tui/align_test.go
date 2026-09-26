package tui

import (
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/events"
	"github.com/moezdil/siltide/internal/kube"
)

// inColor makes the styles emit escapes the way a real terminal sees them.
// Without it lipgloss renders plain text under `go test`, and a table that
// pads a styled cell with a width verb looks fine when it is not.
func inColor(t *testing.T) {
	t.Helper()
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain is what the terminal actually shows, without the colour escapes.
func plain(s string) string { return sgr.ReplaceAllString(s, "") }

// alignDevice is one device carrying metrics of several lengths and two
// processes, one of which reports nothing, so every cell that can be styled
// is styled on one row and bare on the other.
func alignDevice() device.Device {
	d := device.New(device.NVIDIA, 0, "NVIDIA H100 80GB HBM3", "align", "")
	d.Health, d.State = 100, device.StateBusy
	d.Metrics = device.Metrics{
		device.Util: 94, device.Temp: 71, device.Power: 617, device.PowerCap: 700,
		device.MemUsed: 63 << 30, device.MemTotal: 80 << 30, device.ClockCore: 1823,
		device.ClockMem: 2619, device.EccCorrected: 19, device.EccUncorrected: 0,
		device.PCIeGen: 5, device.PCIeWidth: 16, device.NUMANode: 1, device.RemapPending: 0,
	}
	d.Procs = []device.Process{
		{PID: 4000, Name: "python", User: "alice", Namespace: "ml", Pod: "llama-70b-pretrain-0",
			Metrics: device.Metrics{device.MemUsed: 31 << 30, device.Util: 46}},
		{PID: 4001, Name: "vllm", User: "svc", Namespace: "inference", Pod: "chat-api-7d9f8b6c5",
			Metrics: device.Metrics{}}, // reports nothing: every cell reads N/A
	}
	return d
}

// TestDetailColumnsLineUp holds the device detail to its own columns. Width
// verbs count the escapes in a styled value as characters, so a table that
// pads with them drifts as soon as one cell is coloured and another is not.
func TestDetailColumnsLineUp(t *testing.T) {
	inColor(t)
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("default", nil)})
	m.width, m.height = 200, 60
	lines := strings.Split(plain(m.detail(alignDevice())), "\n")

	// the process table: the pod each row names must start at one column
	var starts []int
	for _, l := range lines {
		for _, pod := range []string{"ml/llama-70b-pretrain-0", "inference/chat-api-7d9f8b6c5"} {
			if i := strings.Index(l, pod); i >= 0 {
				starts = append(starts, i)
			}
		}
	}
	if len(starts) != 2 {
		t.Fatalf("found %d process rows, want 2:\n%s", len(starts), strings.Join(lines, "\n"))
	}
	if starts[0] != starts[1] {
		t.Errorf("the pod column starts at %d on one row and %d on the other", starts[0], starts[1])
	}

	// the metric grid: every cell starts on the grid, not where the last
	// value happened to end
	const cell = 37 // 16 for the name, a space, 19 for the value, a space
	keys := []string{"clock_core", "clock_mem", "ecc_corrected", "ecc_uncorrected", "mem_total",
		"mem_used", "numa_node", "pcie_gen", "pcie_width", "power_cap", "remap_pending", "temp", "util", "power"}
	found := 0
	for _, l := range lines {
		if !strings.Contains(l, "  ") {
			continue
		}
		for _, k := range keys {
			for at := 0; ; {
				i := strings.Index(l[at:], k+" ")
				if i < 0 {
					break
				}
				i += at
				at = i + len(k)
				if i > 0 && l[i-1] != ' ' {
					continue // part of a longer name
				}
				found++
				if i%cell != 0 {
					t.Errorf("%s starts at column %d, which is not a multiple of %d:\n%s", k, i, cell, l)
				}
			}
		}
	}
	if found < 6 {
		t.Fatalf("only found %d metric cells to check:\n%s", found, strings.Join(lines, "\n"))
	}
}

// TestProcessPanelColumnsLineUp covers the process lines under the overview,
// where a device with no per-process metrics reads N/A in two cells.
func TestProcessPanelColumnsLineUp(t *testing.T) {
	inColor(t)
	e := demoEngine(t)
	m := New(e, Options{Theme: NewTheme("default", nil)})
	m.width, m.height = 200, 60
	out := plain(m.topProcs([]device.Device{alignDevice()}, 4, 120))

	var ends []int
	for _, l := range strings.Split(out, "\n") {
		if i := strings.Index(l, "llama-70b-pretrain-0"); i >= 0 {
			ends = append(ends, i)
		}
		if i := strings.Index(l, "chat-api-7d9f8b6c5"); i >= 0 {
			ends = append(ends, i)
		}
	}
	if len(ends) != 2 {
		t.Fatalf("found %d rows, want 2:\n%s", len(ends), out)
	}
	if ends[0] != ends[1] {
		t.Errorf("the pod column starts at %d on one row and %d on the other:\n%s", ends[0], ends[1], out)
	}
}

func renderAll(e *collect.Engine) map[string]string {
	out := map[string]string{}
	for tab, k := range TabKeys() {
		m := New(e, Options{Theme: NewTheme("default", nil)})
		mm, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
		m = mm.(Model)
		m.setTab(tab)
		out[k.Name] = plain(m.View())
		m.setSearch("util>1")
		out[k.Name+"/filter"] = plain(m.View())
	}
	return out
}

// TestColorDoesNotMoveColumns renders every tab twice, once with the styles
// on and once with them off, and holds the visible text to being the same.
// A cell padded with a width verb instead of the width-aware helpers drifts
// as soon as it is coloured, and this is what says so.
func TestColorDoesNotMoveColumns(t *testing.T) {
	e := demoEngine(t)
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	bare := renderAll(e)
	lipgloss.SetColorProfile(termenv.TrueColor)
	colored := renderAll(e)
	lipgloss.SetColorProfile(old)

	bare["plain"] = plainSnapshot(e, termenv.Ascii)
	colored["plain"] = plainSnapshot(e, termenv.TrueColor)

	for name, want := range bare {
		got := colored[name]
		if got == want {
			continue
		}
		wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
		for i := range wl {
			if i < len(gl) && wl[i] != gl[i] {
				t.Errorf("%s line %d moves when coloured:\n bare |%s|\n col  |%s|", name, i, wl[i], gl[i])
				break
			}
		}
	}
}

// plainSnapshot renders the --once view under one colour profile.
func plainSnapshot(e *collect.Engine, p termenv.Profile) string {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	defer lipgloss.SetColorProfile(old)
	return plain(Plain(e.Snapshot(), NewTheme("default", nil), 160))
}

// TestSelectedRowKeepsItsHighlight checks the row under the cursor stays
// highlighted the whole way across. Its cells carry their own colours, and
// the reset at the end of each one ends the highlight unless it is put back.
func TestSelectedRowKeepsItsHighlight(t *testing.T) {
	inColor(t)
	th := NewTheme("solarized", nil) // a theme with a real selection colour
	cols := []column{{"NAME", 10, false}, {"BAR", 12, false}, {"STATE", 8, false}}
	rows := [][]string{
		{"first", th.ok.Render("████") + " " + th.dim.Render("░░"), th.warn.Render("busy")},
		{"second", th.ok.Render("██") + " " + th.dim.Render("░░░░"), th.ok.Render("idle")},
	}
	out := th.table(cols, rows, 0, 0, 40, -1, false)

	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(plain(l), "first") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no selected row in:\n%s", out)
	}

	open, _, _ := strings.Cut(th.sel.Render("\x00"), "\x00")
	if open == "" {
		t.Fatal("the selection style writes no escape to check")
	}
	resets := strings.Count(line, reset)
	if got := strings.Count(line, open); got <= 1 {
		t.Errorf("the highlight opens %d time(s) for %d resets, so it stops partway:\n%q", got, resets, line)
	}
	if !strings.HasSuffix(plain(line), " ") {
		t.Errorf("the highlight should run to the end of the row: %q", plain(line))
	}
}

// TestTruncClosesWhatItCuts covers a styled string cut short. The closing
// escape sits at the end, so cutting from the end removes it and the colour
// runs on into the next thing drawn.
func TestTruncClosesWhatItCuts(t *testing.T) {
	inColor(t)
	th := NewTheme("default", nil)
	styled := th.ok.Render("a long green string that will not fit")
	for _, w := range []int{4, 10, 20} {
		got := trunc(styled, w)
		if !strings.HasSuffix(got, reset) {
			t.Errorf("trunc(styled, %d) leaves the colour open: %q", w, got)
		}
		if n := width(got); n > w {
			t.Errorf("trunc(styled, %d) is %d wide", w, n)
		}
	}
	if got := trunc("plain text here", 6); strings.Contains(got, "\x1b") {
		t.Errorf("an unstyled string should stay unstyled: %q", got)
	}
}

// TestSelectionReadsAsOneBar keeps every theme on a selection colour of its
// own: without one the row is drawn in reverse video, which turns the bars
// on it into blocks of solid colour instead of a highlight.
func TestSelectionReadsAsOneBar(t *testing.T) {
	for _, name := range ThemeNames() {
		p := palettes[name]
		if p["selection_bg"] == "" {
			t.Errorf("theme %s sets no selection_bg, so its selected row inverts", name)
		}
	}
}

// TestKubeEmptyStateNamesWhatWasTried covers the tab someone opens on a
// machine with no cluster: it should say where siltide looked, not only that
// it found nothing.
func TestKubeEmptyStateNamesWhatWasTried(t *testing.T) {
	m := Model{th: NewTheme("mono", nil), width: 120}
	out := m.unreachable("Kubernetes was not detected.", []kube.Attempt{
		{What: "pod log directory", Where: "/var/log/pods", Err: "stat /var/log/pods: no such file or directory"},
		{What: "in-cluster service account", Where: "/var/run/secrets/kubernetes.io/serviceaccount", Err: "KUBERNETES_SERVICE_HOST is not set: not running in a pod"},
		{What: "kubeconfig", Where: "/home/me/.kube/config", Err: ""},
	})
	for _, want := range []string{"Kubernetes was not detected.", "what was tried", "pod log directory", "/var/log/pods", "no such file", "service account", "not running in a pod", "kubeconfig", "ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("the empty state does not mention %q:\n%s", want, out)
		}
	}
	if m.unreachable("nothing here.", nil) == "" {
		t.Error("an empty attempt list should still carry the headline")
	}
}

// TestEventKindTally covers the summary above the event log: what the log is
// made of, most frequent first, coloured by the worst event in each kind.
func TestEventKindTally(t *testing.T) {
	m := Model{th: NewTheme("mono", nil), width: 120}
	evs := []events.Event{
		{Kind: "thermal", Severity: events.Warning},
		{Kind: "thermal", Severity: events.Critical},
		{Kind: "thermal", Severity: events.Info},
		{Kind: "ecc", Severity: events.Info},
		{Kind: "ecc", Severity: events.Info},
		{Kind: "xid", Severity: events.Critical},
	}
	got := plain(m.kindTally(evs))
	if !strings.Contains(got, "3 thermal") || !strings.Contains(got, "2 ecc") || !strings.Contains(got, "1 xid") {
		t.Fatalf("tally %q", got)
	}
	if i, j, k := strings.Index(got, "thermal"), strings.Index(got, "ecc"), strings.Index(got, "xid"); i >= j || j >= k {
		t.Errorf("kinds are not ordered by frequency: %q", got)
	}
	if m.kindTally(evs[:1]) != "" {
		t.Error("a single event needs no summary")
	}
	if m.kindTally(nil) != "" {
		t.Error("an empty log needs no summary")
	}

	// the colour of a kind follows its worst event
	inColor(t)
	coloured := m.kindTally(evs)
	crit, _, _ := strings.Cut(NewTheme("mono", nil).crit.Render("x"), "x")
	if crit != "" && !strings.Contains(coloured, crit) {
		t.Errorf("no critical colour in the tally: %q", coloured)
	}
}
