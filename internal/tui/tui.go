// Package tui is the interactive terminal interface.
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/device"
)

// tabs in display order; the key switches to the tab.
var tabs = []struct{ key, name, short string }{
	{"1", "Overview", "Overview"}, {"2", "Devices", "Devices"}, {"3", "Processes", "Procs"}, {"4", "Memory", "Memory"}, {"5", "Power", "Power"},
	{"6", "Thermals", "Thermal"}, {"7", "Links", "Links"}, {"8", "History", "History"}, {"9", "Events", "Events"}, {"0", "Nodes", "Nodes"},
	{"N", "Network", "Net"}, {"K", "Kubernetes", "Kube"}, {"W", "Workloads", "Work"}, {"D", "Dashboard", "Dash"}, {"H", "Health", "Health"}, {"?", "Help", "Help"},
}

const (
	tabOverview = iota
	tabDevices
	tabProcesses
	tabMemory
	tabPower
	tabThermals
	tabLinks
	tabHistory
	tabEvents
	tabNodes
	tabNetwork
	tabKube
	tabWorkloads
	tabDashboard
	tabHealth
	tabHelp
)

// overlays sit on top of a tab.
const (
	overlayNone = iota
	overlayDescribe
	overlayLogs
	overlayCompare
	overlaySelfLog
)

type refreshed struct{}

// Options configure the interface.
type Options struct {
	Theme    Theme
	Keys     Keymap
	TempWarn float64 // for the thermals view; 0 means 85
	Mouse    bool
	Currency string
	// LogFile is siltide's own log, shown by `:log`. Empty means logging is
	// off, which the view says rather than showing nothing.
	LogFile string
	// Open is the view the command line asked for. When it asks for nothing
	// the interface returns to where it was last closed.
	Open openView
	// NoSession leaves the last view unread and unwritten, for a run that
	// should not disturb it: --replay, --demo, a recording.
	NoSession bool
	// Reload re-reads the config file and returns the options it now asks
	// for, after applying it to the engine. nil disables `:reload`.
	Reload func() (Options, error)
}

// Model is the Bubble Tea model.
type Model struct {
	eng      *collect.Engine
	snap     collect.Snapshot
	th       Theme
	keys     Keymap
	tempWarn float64
	currency string
	reload   func() (Options, error)
	marks    []Bookmark
	logFile  string
	session  bool // remember the view for the next run
	tab      int
	prev     int // tab to return to from help
	sel      int // selected row on the current tab
	sels     map[int]int
	marked   map[string]bool // rows marked with x, by identity rather than position
	paused   bool
	width    int
	height   int

	// filters and sorting
	search   string
	filter   Filter
	node     string
	ns       string
	sortCol  int // column index of the current tab's table
	sortDesc bool

	// command bar
	cmdOpen    bool
	searchOpen bool
	cmdSel     int // suggestion under the cursor while the command bar is open
	input      string
	notice     string
	noticeAt   time.Time

	// history time machine; zero cursor means live
	cursor     time.Time
	histMetric int
	window     time.Duration
	pick       int // device picked in the inspector, -1 for all

	// overlays
	overlay   int
	text      string // describe or logs body
	textTitle string
	scroll    int
	wrap      bool
	container int
	compare   [2]string

	// detail pane scroll (Devices tab), in lines
	detailScroll int

	// mouse
	tabSpans  []span
	headSpans []span
	lastClick time.Time
	lastRow   int
}

type span struct{ x0, x1, tab int }

// Run shows the TUI until the user quits.
func Run(ctx context.Context, eng *collect.Engine, o Options) error {
	m := New(eng, o)
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	if o.Mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	final, err := tea.NewProgram(m, opts...).Run()
	// Where the interface was when it closed, for the next run to open on.
	// A failure to write it is not worth reporting over whatever ended the
	// program.
	if last, ok := final.(Model); ok && last.session {
		_ = last.saveSession()
	}
	return err
}

// New builds a model.
func New(eng *collect.Engine, o Options) Model {
	m := Model{eng: eng, snap: eng.Snapshot(), width: 100, height: 30, sels: map[int]int{},
		sortDesc: true, window: 30 * time.Minute, pick: -1}
	m.apply(o)
	m.marks = loadBookmarks()
	m.session = !o.NoSession
	switch {
	case !o.Open.Empty():
		m.openFromFlags(o.Open)
	case m.session:
		if s, ok := loadSession(); ok {
			m.restore(s)
		}
	}
	return m
}

// apply takes the settings of o, filling in the defaults for the ones it
// leaves empty. It runs at startup and again on every `:reload`.
func (m *Model) apply(o Options) {
	m.th, m.keys, m.tempWarn, m.currency, m.reload = o.Theme, o.Keys, o.TempWarn, o.Currency, o.Reload
	m.logFile = o.LogFile
	if m.keys == nil {
		m.keys = NewKeymap(nil)
	}
	if m.tempWarn == 0 {
		m.tempWarn = 85
	}
	if m.currency == "" {
		m.currency = "$"
	}
}

func (m Model) wait() tea.Cmd {
	ch := m.eng.Changed()
	return func() tea.Msg { <-ch; return refreshed{} }
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return m.wait() }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case refreshed:
		if !m.paused {
			m.snap = m.eng.Snapshot()
		}
		return m, m.wait()
	case tea.MouseMsg:
		m.mouse(msg)
	case tea.KeyMsg:
		if m.cmdOpen || m.searchOpen {
			return m.typing(msg), nil
		}
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) mouse(msg tea.MouseMsg) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		switch {
		case m.overlay != overlayNone:
			m.scroll = max(m.scroll-3, 0)
		case m.tab == tabDevices && msg.Y > m.rowsTop()+m.tableHeight():
			m.detailScroll = max(m.detailScroll-3, 0) // the pane under the pointer
		default:
			m.move(-1)
		}
	case tea.MouseButtonWheelDown:
		switch {
		case m.overlay != overlayNone:
			m.scroll += 3
		case m.tab == tabDevices && msg.Y > m.rowsTop()+m.tableHeight():
			m.detailScroll += 3
		default:
			m.move(1)
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return
		}
		if msg.Y == 1 {
			for _, s := range m.tabSpans {
				if msg.X >= s.x0 && msg.X < s.x1 {
					m.overlay = overlayNone
					m.setTab(s.tab)
				}
			}
			return
		}
		if msg.Y == m.height-1 { // footer hints run their key
			for _, s := range m.hintSpans() {
				if msg.X >= s.x0 && msg.X < s.x1 {
					m.hint(s.tab)
				}
			}
			return
		}
		top := m.rowsTop()
		if top > 0 && msg.Y == top-1 { // column header: sort by it
			for _, s := range m.headSpans {
				if msg.X >= s.x0 && msg.X < s.x1 {
					m.sortBy(s.tab)
				}
			}
			return
		}
		if top > 0 {
			if row := msg.Y - top; row >= 0 && row < m.rowCount() {
				row += m.scrollStart()
				if row == m.lastRow && time.Since(m.lastClick) < 400*time.Millisecond {
					m.open()
				}
				m.lastRow, m.lastClick = row, time.Now()
				m.setSel(row)
			}
		}
	}
}

// hint runs the i-th footer hint.
func (m *Model) hint(i int) {
	hints := m.hintKeys()
	if i < len(hints) {
		var msg tea.KeyMsg
		switch hints[i] {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(hints[i])}
		}
		mm, _ := m.key(msg)
		*m = mm.(Model)
	}
}

// open acts on the selected row: device detail, pod describe, and event device.
func (m *Model) open() {
	switch m.tab {
	case tabOverview, tabMemory, tabPower, tabThermals, tabLinks:
		m.setTab(tabDevices)
	case tabKube:
		m.describe()
	case tabEvents:
		evs := m.eventRows()
		if m.sel < len(evs) {
			if i := m.deviceIndex(evs[m.sel].Device); i >= 0 {
				m.sels[tabOverview] = i
				m.setTab(tabDevices)
			}
		}
	case tabProcesses:
		rows := m.processes()
		if m.sel < len(rows) {
			if i := m.deviceIndex(rows[m.sel].d.ID); i >= 0 {
				m.sels[tabOverview] = i
				m.setTab(tabDevices)
			}
		}
	}
}

func (m Model) deviceIndex(id string) int {
	for i, d := range m.devices() {
		if d.ID == id {
			return i
		}
	}
	return -1
}

func (m Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if m.overlay != overlayNone {
		return m.overlayKey(k), nil
	}
	for i, t := range tabs {
		if k == t.key {
			m.setTab(i)
			return m, nil
		}
	}
	act := m.keys[k]
	if m.tab == tabHistory || m.tab == tabDashboard {
		switch act {
		case ActHistBack:
			m.scrub(-1)
			return m, nil
		case ActHistFwd:
			m.scrub(1)
			return m, nil
		case ActHistJump:
			if k == "<" {
				m.scrub(-30)
			} else {
				m.scrub(30)
			}
			return m, nil
		case ActHistLive:
			m.cursor = time.Time{}
			return m, nil
		case ActMetric:
			m.histMetric = (m.histMetric + 1) % len(histMetrics)
			return m, nil
		case ActMetricPr:
			m.histMetric = (m.histMetric + len(histMetrics) - 1) % len(histMetrics)
			return m, nil
		case ActZoomIn:
			m.zoom(-1)
			return m, nil
		case ActZoomOut:
			m.zoom(1)
			return m, nil
		case ActPrevDev:
			m.pick = max(m.pick-1, -1)
			return m, nil
		case ActNextDev:
			m.pick = min(m.pick+1, len(m.devices())-1)
			return m, nil
		}
	}
	switch act {
	case ActQuit:
		return m, tea.Quit
	case ActUp:
		m.move(-1)
	case ActDown:
		m.move(1)
	case ActPageUp:
		m.move(-10)
	case ActPageDown:
		m.move(10)
	case ActTop:
		m.setSel(0)
	case ActBottom:
		m.setSel(m.rowCount() - 1)
	case ActEnter:
		m.open()
	case ActBack:
		switch {
		case m.search != "" || m.node != "" || m.ns != "":
			m.search, m.filter, m.node, m.ns = "", Filter{}, "", ""
		case m.tab == tabHelp:
			m.setTab(m.prev)
		case m.tab != tabOverview:
			m.setTab(tabOverview)
		}
	case ActNextTab:
		m.setTab(m.nextVisible(1))
	case ActPrevTab:
		m.setTab(m.nextVisible(-1))
	case ActPause:
		m.paused = !m.paused
		if !m.paused {
			m.snap = m.eng.Snapshot()
		}
	case ActRefresh:
		m.eng.Refresh()
	case ActHelp:
		m.setTab(tabHelp)
	case ActCommand:
		m.cmdOpen, m.input = true, ""
	case ActSearch:
		m.searchOpen, m.input = true, m.search
	case ActSortNext:
		m.sortBy((m.sortCol + 1) % max(len(m.columns()), 1))
	case ActSortRev:
		m.sortDesc = !m.sortDesc
	case ActDescribe:
		if m.tab == tabKube {
			m.describe()
		}
	case ActLogs:
		if m.tab == tabKube {
			m.logs()
		}
	case ActDash:
		m.setTab(tabDashboard)
	case ActNextNode:
		m.cycleNode(1)
	case ActPrevNode:
		m.cycleNode(-1)
	case ActExport:
		m.exportCSV()
	case ActMark:
		m.toggleMark()
	case ActMarkAll:
		m.markAll()
	case ActYank:
		return m, m.yankIDs()
	case ActYankCmd:
		return m, m.yankCommand()
	}
	return m, nil
}

// cycleNode steps the node filter through "", local, and every remote node.
func (m *Model) cycleNode(dir int) {
	names := []string{""}
	seen := map[string]bool{}
	for _, d := range m.snap.Devices {
		n := d.Node
		if n == "" {
			n = "local"
		}
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	i := 0
	for j, n := range names {
		if n == m.node {
			i = j
		}
	}
	m.node = names[(i+dir+len(names))%len(names)]
	m.sel = 0
	if m.node == "" {
		m.say("all nodes")
	} else {
		m.say("node " + m.node)
	}
}

// exportCSV writes the history of the devices in view to a CSV file in the current directory.
func (m *Model) exportCSV() {
	h := m.eng.History()
	if h == nil {
		m.say("history is disabled")
		return
	}
	path := "siltide-history-" + time.Now().Format("20060102-150405") + ".csv"
	f, err := createFile(path)
	if err != nil {
		m.say(err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	n, err := ExportCSV(f, h, m.markedDevices(m.devices()))
	if err != nil {
		m.say(err.Error())
		return
	}
	m.say(fmt.Sprintf("wrote %d rows to %s", n, path))
}

// nextVisible steps over hidden tabs.
func (m Model) nextVisible(dir int) int {
	t := m.tab
	for i := 0; i < len(tabs); i++ {
		t = (t + dir + len(tabs)) % len(tabs)
		if m.tabVisible(t) {
			return t
		}
	}
	return m.tab
}

func (m Model) overlayKey(k string) Model {
	switch m.keys[k] {
	case ActBack, ActQuit:
		m.overlay, m.scroll = overlayNone, 0
	case ActUp:
		m.scroll = max(m.scroll-1, 0)
	case ActDown:
		m.scroll++
	case ActPageUp:
		m.scroll = max(m.scroll-10, 0)
	case ActPageDown:
		m.scroll += 10
	case ActTop:
		m.scroll = 0
	case ActWrap:
		m.wrap = !m.wrap
	case ActNextCont:
		if m.overlay == overlayLogs {
			m.container++
			m.logs()
		}
	case ActLogs:
		if m.overlay == overlayDescribe {
			m.logs()
		}
	case ActDescribe:
		if m.overlay == overlayLogs {
			m.describe()
		}
	case ActRefresh:
		switch m.overlay {
		case overlayLogs:
			m.logs()
		case overlayDescribe:
			m.describe()
		case overlaySelfLog:
			m.selfLog()
		}
	}
	return m
}

func (m Model) typing(msg tea.KeyMsg) Model {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.cmdOpen, m.searchOpen = false, false
	case "enter":
		if m.cmdOpen {
			m.command(strings.TrimSpace(m.input))
		} else {
			m.setSearch(strings.TrimSpace(m.input))
		}
		m.cmdOpen, m.searchOpen = false, false
	case "backspace":
		if len(m.input) > 0 {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
		}
		m.cmdSel = 0
	case "tab":
		if m.cmdOpen {
			m.input, m.cmdSel = m.complete(m.input), 0
		}
	case "up", "ctrl+p":
		if m.cmdOpen {
			m.cmdSel = max(m.cmdSel-1, 0)
		}
	case "down", "ctrl+n":
		if m.cmdOpen {
			m.cmdSel = min(m.cmdSel+1, max(len(m.suggestions(m.input))-1, 0))
		}
	default:
		if msg.Type == tea.KeyRunes || msg.String() == " " {
			m.input, m.cmdSel = m.input+msg.String(), 0
		}
	}
	if m.searchOpen {
		m.setSearch(strings.TrimSpace(m.input)) // live filtering
	}
	return m
}

func (m *Model) setSearch(s string) {
	m.search = s
	m.filter = ParseFilter(s)
	m.sel = 0
}

// command runs a ":" command.
func (m *Model) command(line string) {
	if line == "" {
		return
	}
	f := strings.Fields(line)
	cmd, arg := f[0], strings.Join(f[1:], " ")
	for i, t := range tabs {
		if strings.EqualFold(cmd, t.name) || (len(cmd) >= 3 && strings.HasPrefix(strings.ToLower(t.name), strings.ToLower(cmd))) {
			m.setTab(i)
			return
		}
	}
	switch cmd {
	case "q", "quit":
		m.say("use q to quit")
	case "tab":
		m.command(arg)
	case "sort":
		for i, c := range m.columns() {
			if strings.EqualFold(strings.TrimSpace(c.name), arg) {
				m.sortBy(i)
				return
			}
		}
		m.say("sort " + strings.ToLower(strings.Join(m.columnNames(), "|")))
	case "filter", "search":
		m.setSearch(arg)
	case "node":
		m.node = arg
	case "ns", "namespace":
		m.ns = arg
		m.setTab(tabKube)
	case "metric":
		for i, h := range histMetrics {
			if h.name == arg {
				m.histMetric = i
				return
			}
		}
		m.say("metric util|memory|power|temp|clock")
	case "window":
		if d, err := time.ParseDuration(arg); err == nil && d > 0 {
			m.window = d
		} else {
			m.say("window 15m|1h|6h|24h")
		}
	case "theme":
		t, err := LoadTheme(arg, themeDir(), nil, m.th.Transparent)
		if err != nil {
			m.say(err.Error() + "; built in: " + strings.Join(ThemeNames(), ", "))
			return
		}
		m.th = t
	case "compare":
		m.startCompare(f[1:])
	case "reload":
		m.reloadConfig()
	case "log":
		m.selfLog()
	case "bookmark", "bm":
		m.bookmark(arg)
	case "pause":
		m.paused = !m.paused
	case "refresh":
		m.eng.Refresh()
	case "live":
		m.cursor = time.Time{}
	case "describe", "logs":
		if arg != "" {
			m.jumpPod(arg)
		}
		if cmd == "logs" {
			m.logs()
		} else {
			m.describe()
		}
	default:
		if m.jumpPod(cmd) {
			return
		}
		m.say("unknown command: " + cmd)
	}
}

// reloadConfig re-reads the config file and applies it to the engine and to
// this model, so a theme, threshold, or key binding takes effect without a
// restart. A file that no longer parses leaves everything as it was.
func (m *Model) reloadConfig() {
	if m.reload == nil {
		m.say("reload is only available when a config file is in use")
		return
	}
	o, err := m.reload()
	if err != nil {
		m.say("reload: " + err.Error())
		return
	}
	m.apply(o)
	m.say("config reloaded")
}

// selfLogLines is how much of siltide's own log the view holds. Long enough
// to cover a start and the failures after it, short enough to open at once.
const selfLogLines = 2000

// selfLog shows the tail of siltide's own log, which otherwise only exists
// as a file someone has to quit the interface to read.
func (m *Model) selfLog() {
	if m.logFile == "" {
		m.say("logging is off: start with --debug, or set log: in the config")
		return
	}
	b, err := os.ReadFile(m.logFile)
	if err != nil {
		m.say("log: " + err.Error())
		return
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > selfLogLines {
		lines = lines[len(lines)-selfLogLines:]
	}
	text := strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		text = "the log file is empty"
	}
	m.text, m.textTitle, m.overlay = text, "log "+m.logFile, overlaySelfLog
	m.scroll = max(len(lines)-(m.height-5), 0) // the newest lines first
}

// jumpPod selects a pod by name on the Kubernetes tab.
func (m *Model) jumpPod(name string) bool {
	for i, p := range m.pods() {
		if strings.Contains(p.name, name) {
			m.setTab(tabKube)
			m.sel = i
			return true
		}
	}
	return false
}

func (m *Model) say(s string) { m.notice, m.noticeAt = s, time.Now() }

func (m *Model) sortBy(col int) {
	if m.sortCol == col {
		m.sortDesc = !m.sortDesc
	} else {
		m.sortCol, m.sortDesc = col, true
	}
}

// selGroup shares one selection between every tab that lists devices.
func selGroup(tab int) int {
	switch tab {
	case tabOverview, tabDevices, tabMemory, tabPower, tabThermals, tabLinks, tabHistory, tabDashboard, tabHealth:
		return tabOverview
	}
	return tab
}

func (m *Model) setTab(i int) {
	if i == tabHelp && m.tab != tabHelp {
		m.prev = m.tab
	}
	if selGroup(i) != selGroup(m.tab) {
		m.sortCol, m.sortDesc = 0, true
	}
	m.sels[selGroup(m.tab)] = m.sel
	m.tab = i
	m.sel = m.sels[selGroup(i)]
	m.overlay = overlayNone
	m.detailScroll = 0
}

// rowsTop is the screen line of the first table row on the current tab,
// or 0 when the tab has no clickable rows. Header and tab bar take 2 lines.
func (m Model) rowsTop() int {
	switch m.tab {
	case tabOverview:
		return 5 // fleet line, derived line, table header
	case tabDevices:
		return 3
	case tabEvents:
		return 6 + len(m.snap.Alerts)
	case tabHelp, tabHistory, tabDashboard:
		return 0
	default:
		return 4 // title or info line, table header
	}
}

func (m *Model) move(d int)   { m.setSel(m.sel + d) }
func (m *Model) setSel(i int) { m.sel = min(max(i, 0), max(m.rowCount()-1, 0)) }

// scrollStart is the first visible row of the current table.
func (m Model) scrollStart() int {
	h := m.tableHeight()
	if h > 0 && m.sel >= h {
		return m.sel - h + 1
	}
	return 0
}

func (m Model) tableHeight() int {
	switch m.tab {
	case tabOverview:
		return max(m.height-14, 5)
	case tabDevices:
		return max(m.height/3, 4)
	default:
		return max(m.height-8, 3)
	}
}

// zoom widens or narrows the history window.
func (m *Model) zoom(dir int) {
	steps := []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}
	i := 0
	for j, s := range steps {
		if s <= m.window {
			i = j
		}
	}
	m.window = steps[min(max(i+dir, 0), len(steps)-1)]
}

// scrub moves the history cursor by steps of the store's resolution.
func (m *Model) scrub(steps int) {
	h := m.eng.History()
	if h == nil {
		return
	}
	lo, hi := h.Span()
	if hi.IsZero() {
		return
	}
	cur := m.cursor
	if cur.IsZero() {
		cur = hi
	}
	cur = cur.Add(time.Duration(steps) * h.Resolution())
	switch {
	case cur.Before(lo):
		cur = lo
	case !cur.Before(hi):
		cur = time.Time{}
	}
	m.cursor = cur
}

// devices returns the devices in view after node, filter, and sort.
func (m Model) devices() []device.Device {
	var out []device.Device
	for _, d := range m.snap.Devices {
		if m.node != "" && d.Node != m.node && (m.node != "local" || d.Node != "") {
			continue
		}
		if m.tab != tabProcesses && m.tab != tabEvents && !m.filter.Empty() && !m.filter.Device(d) {
			continue
		}
		out = append(out, d)
	}
	if selGroup(m.tab) == tabOverview && m.sortCol > 0 {
		key := deviceSortKeys[min(m.sortCol, len(deviceSortKeys)-1)]
		sort.SliceStable(out, func(i, j int) bool {
			a, b := key(out[i]), key(out[j])
			if m.sortDesc {
				return a > b
			}
			return a < b
		})
	}
	return out
}

// deviceSortKeys follow the device table columns.
var deviceSortKeys = []func(device.Device) float64{
	func(d device.Device) float64 { return float64(d.Index) },
	func(d device.Device) float64 { return float64(len(d.Vendor)) },
	func(d device.Device) float64 { return float64(len(d.Name)) },
	func(d device.Device) float64 { return d.Metrics.Or(device.Util, -1) },
	func(d device.Device) float64 { p, _ := d.MemPercent(); return p },
	func(d device.Device) float64 { return d.Metrics.Or(device.MemUsed, -1) },
	func(d device.Device) float64 { return d.Metrics.Or(device.Temp, -1) },
	func(d device.Device) float64 { return d.Metrics.Or(device.Power, -1) },
	func(d device.Device) float64 { return float64(d.Health) },
	func(d device.Device) float64 { return float64(len(d.State)) },
	func(d device.Device) float64 { return float64(len(d.Procs)) },
}

// View implements tea.Model.
func (m Model) View() string {
	body := m.body()
	if m.overlay != overlayNone {
		body = m.viewOverlay()
	}
	lines := strings.Split(body, "\n")
	// The suggestion list sits between the body and the bar, so the body
	// gives up exactly the rows it takes and the interface keeps its height.
	if keep := m.height - 3 - m.suggestionRows(); keep > 0 && len(lines) > keep {
		lines = lines[:keep]
	}
	for i := range lines {
		if width(lines[i]) > m.width {
			lines[i] = trunc(lines[i], m.width)
		}
	}
	return m.header() + "\n" + m.tabBar() + "\n" + strings.Join(lines, "\n") + "\n" + m.footer()
}

func (m Model) header() string {
	s, th := m.snap, m.th
	nodes := map[string]bool{}
	for _, d := range s.Devices {
		nodes[d.Node] = true
	}
	parts := []string{th.title.Render("siltide"), s.Host, fmt.Sprintf("%d devices", len(s.Devices))}
	if len(nodes) > 1 {
		parts = append(parts, fmt.Sprintf("%d nodes", len(nodes)))
	}
	if s.Demo {
		parts = append(parts, th.warn.Render("DEMO: simulated data"))
	}
	if m.paused {
		parts = append(parts, th.mid.Render("PAUSED"))
	}
	if !m.cursor.IsZero() {
		parts = append(parts, th.mid.Render("TIME MACHINE "+m.cursor.Format("15:04:05")))
	}
	if n := len(s.Alerts); n > 0 {
		parts = append(parts, th.warn.Render(fmt.Sprintf("▲ %d alerts", n)))
	}
	left := strings.Join(parts, th.dim.Render("  │  "))
	clock := s.Time.Format("15:04:05")
	if s.Time.IsZero() {
		clock = "starting"
	}
	if m.width < 80 {
		left = th.title.Render("siltide") + " " + fmt.Sprintf("%d devices", len(s.Devices))
	}
	return left + rpad(th.dim.Render(clock), max(m.width-width(left), 0))
}

func (m *Model) tabBarSpans() string {
	var b strings.Builder
	m.tabSpans = m.tabSpans[:0]
	x := 0
	// Shorten the names until the bar fits; a wrapped bar breaks every view.
	// Rungs: full names, short names, 3 letters, keys only.
	nameOf := func(i, rung int) string {
		t := tabs[i]
		switch rung {
		case 0:
			return t.name
		case 1:
			return t.short
		case 2:
			return t.name[:3]
		}
		return ""
	}
	rung := 0
	for ; rung < 3; rung++ {
		total := 0
		for i := range tabs {
			if m.tabVisible(i) {
				total += width(nameOf(i, rung)) + 4 // key, 2 spaces, and the gap
			}
		}
		if total <= m.width {
			break
		}
	}
	for i, t := range tabs {
		if !m.tabVisible(i) {
			continue
		}
		name := nameOf(i, rung)
		label := " " + t.key + " " + name + " "
		if i == m.tab {
			b.WriteString(m.th.tab.Render(label))
		} else {
			b.WriteString(m.th.dim.Render(t.key) + " " + name + "  ")
		}
		w := width(label) + 1
		m.tabSpans = append(m.tabSpans, span{x, x + w, i})
		x += w
	}
	return b.String()
}

// tabVisible hides tabs with nothing to show: Links without any link or
// PCIe data, Kubernetes without a pod source, and Network without interfaces.
func (m Model) tabVisible(i int) bool {
	switch i {
	case tabLinks:
		for _, d := range m.snap.Devices {
			if len(d.Links) > 0 || d.Metrics.Or(device.PCIeGen, 0) > 0 || d.Metrics.Or(device.Partitions, 0) > 0 {
				return true
			}
		}
		return m.tab == tabLinks
	case tabKube:
		return m.snap.Kube || m.snap.Demo || len(m.pods()) > 0 || m.tab == tabKube
	case tabNetwork:
		return (m.snap.Host2 != nil && len(m.snap.Host2.Nets) > 0) || m.tab == tabNetwork
	}
	return true
}

func (m Model) tabBar() string { return (&m).tabBarSpans() }

func (m Model) footer() string {
	th := m.th
	switch {
	case m.cmdOpen:
		return m.suggestionList() + th.accent.Render(":") + m.input + "█"
	case m.searchOpen:
		return th.accent.Render("/") + m.input + "█"
	}
	keys := m.hints()
	right := ""
	switch {
	case m.notice != "" && time.Since(m.noticeAt) < 5*time.Second:
		right = th.warn.Render(m.notice)
	case m.search != "" || m.node != "" || m.ns != "":
		right = th.dim.Render(fmt.Sprintf("filter %q node %q ns %q", m.search, m.node, m.ns))
	case len(m.snap.Warnings) > 0:
		right = th.mid.Render(trunc(m.snap.Warnings[0], 40))
	default:
		if inactive := m.inactive(); inactive > 0 {
			right = th.dim.Render(fmt.Sprintf("%d vendors not present (?)", inactive))
		}
	}
	return th.dim.Render(keys) + rpad(right, max(m.width-width(keys), 0))
}

func (m Model) inactive() int {
	n := 0
	for _, p := range m.snap.Providers {
		if !p.Active {
			n++
		}
	}
	return n
}

// hintKeys are the keys behind the footer hints, in the order shown.
func (m Model) hintKeys() []string {
	switch {
	case m.overlay == overlayLogs:
		return []string{"esc", "d", "c", "w", "r"}
	case m.overlay == overlayDescribe:
		return []string{"esc", "l", "r"}
	case m.overlay == overlaySelfLog:
		return []string{"esc", "r", "w"}
	case m.overlay != overlayNone:
		return []string{"esc"}
	case m.tab == tabHistory, m.tab == tabDashboard:
		return []string{",", ".", "n", "m", "+", "-", "{", "}", ":"}
	case m.tab == tabKube:
		return []string{"enter", "l", "/", ":", "?"}
	case m.tab == tabProcesses:
		return []string{"s", "S", "/", ":", "?"}
	}
	return []string{"enter", "tab", "p", "r", "/", ":", "?", "q"}
}

// hintSpans maps footer x ranges to hint indices; hints are words
// separated by 2 spaces whose first token is the key.
func (m Model) hintSpans() []span {
	var out []span
	x := 0
	for i, h := range strings.Split(m.hints(), "  ") {
		w := width(h)
		out = append(out, span{x, x + w, i})
		x += w + 2
	}
	return out
}

func (m Model) hints() string {
	switch {
	case m.overlay == overlayLogs:
		return "esc back  d describe  c next container  w wrap  r reload  ↑↓ scroll"
	case m.overlay == overlayDescribe:
		return "esc back  l logs  r reload  ↑↓ scroll"
	case m.overlay == overlayCompare:
		return "esc back"
	case m.tab == tabHistory, m.tab == tabDashboard:
		return ", back  . forward  n now  m metric  + narrower  - wider  { prev device  } next device  : command"
	case m.tab == tabKube:
		return "⏎ describe  l logs  / search  : ns NAME  ? help"
	case m.tab == tabProcesses:
		return "s sort  S reverse  / search  : command  ? help"
	default:
		return "⏎ detail  tab next  p pause  r refresh  / search  : command  ? help  q quit"
	}
}

func (m Model) body() string {
	switch m.tab {
	case tabDevices:
		return m.viewDevices()
	case tabProcesses:
		return (&m).viewProcesses()
	case tabMemory:
		return m.viewMemory()
	case tabPower:
		return m.viewPower()
	case tabThermals:
		return m.viewThermals()
	case tabLinks:
		return m.viewLinks()
	case tabHistory:
		return m.viewHistory()
	case tabEvents:
		return (&m).viewEvents()
	case tabNodes:
		return (&m).viewNodes()
	case tabNetwork:
		return (&m).viewNetwork()
	case tabKube:
		return (&m).viewKube()
	case tabWorkloads:
		return (&m).viewWorkloads()
	case tabDashboard:
		return m.viewDashboard()
	case tabHealth:
		return (&m).viewHealth()
	case tabHelp:
		return m.viewHelp()
	default:
		return m.viewOverview()
	}
}

// rowCount is how many selectable rows the current tab has.
func (m Model) rowCount() int {
	switch m.tab {
	case tabProcesses:
		return len(m.processes())
	case tabEvents:
		return len(m.eventRows())
	case tabNodes:
		return len(m.nodes())
	case tabNetwork:
		return len(m.netRows())
	case tabKube:
		return len(m.pods())
	case tabWorkloads:
		return len(m.workloads())
	case tabHelp, tabHistory, tabDashboard:
		return 0
	default:
		return len(m.devices())
	}
}

// columns returns the sortable columns of the current tab.
func (m Model) columns() []column {
	switch m.tab {
	case tabProcesses:
		return procColumns(m.width)
	case tabOverview, tabDevices:
		return deviceColumns(m.width, m.devices())
	}
	return nil
}

func (m Model) columnNames() []string {
	var out []string
	for _, c := range m.columns() {
		if c.name != "" {
			out = append(out, c.name)
		}
	}
	return out
}

func themeDir() string {
	home, _ := userHome()
	return home + "/.config/siltide/themes"
}

// startCompare sets up a side-by-side view of 2 devices or of one device
// at 2 times (":compare 3 5", ":compare 3 15:04").
func (m *Model) startCompare(args []string) {
	if len(args) == 0 {
		args = []string{strconv.Itoa(m.sel)}
	}
	if len(args) == 1 {
		args = append(args, "")
	}
	m.compare = [2]string{args[0], args[1]}
	m.overlay = overlayCompare
	m.scroll = 0
}

// Tab names a tab and the key that opens it.
type Tab struct{ Key, Name string }

// TabKeys lists the tabs in order, for tools that drive the model.
func TabKeys() []Tab {
	out := make([]Tab, len(tabs))
	for i, t := range tabs {
		out[i] = Tab{t.key, t.name}
	}
	return out
}
