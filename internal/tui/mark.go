package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/moezdil/siltide/internal/device"
)

// Marking rows, and handing the command for them to the shell.
//
// siltide never sends a signal. It is installed on machines people share, and
// the thing that makes it safe to leave there is that it cannot change them.
// What it can do is stop making an operator retype what they already pointed
// at: mark the rows, take `kill 4000 4001` or `kubectl delete pod a b -n ml`,
// and run it yourself, in your shell, with your credentials and your history.
//
// A mark is held by identity rather than by position, so it survives a sort, a
// filter, a refresh and a tab change, and a row that goes away takes its mark
// with it.

// markRow is one row that can be marked: its identity, what it is called on
// the command line, and which namespace it belongs to when it has one.
type markRow struct {
	key string // stable across sorts and refreshes
	id  string // the pid, pod name or device id
	ns  string // kubernetes namespace, for the rows that have one
}

// markable lists the rows of the current tab that can be marked, in the order
// they are drawn. An empty result means this tab does not mark.
func (m Model) markable() []markRow {
	var out []markRow
	switch m.tab {
	case tabProcesses:
		for _, r := range m.processes() {
			out = append(out, markRow{
				key: fmt.Sprintf("p:%s/%d/%d", r.d.Node, r.d.Index, r.p.PID),
				id:  fmt.Sprint(r.p.PID),
				ns:  r.p.Namespace,
			})
		}
	case tabKube:
		for _, r := range m.pods() {
			out = append(out, markRow{key: "k:" + r.ns + "/" + r.name, id: r.name, ns: r.ns})
		}
	case tabDevices, tabOverview:
		for _, d := range m.devices() {
			out = append(out, markRow{key: "d:" + d.ID, id: d.ID})
		}
	}
	return out
}

// marksHere is the marked rows of this tab, in the order they are drawn.
func (m Model) marksHere() []markRow {
	var out []markRow
	for _, r := range m.markable() {
		if m.marked[r.key] {
			out = append(out, r)
		}
	}
	return out
}

// markedRows is the set of drawn row indices that carry a mark, which is what
// the table renderer needs.
func (m Model) markedRows() map[int]bool {
	if len(m.marked) == 0 {
		return nil
	}
	out := map[int]bool{}
	for i, r := range m.markable() {
		if m.marked[r.key] {
			out[i] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// toggleMark marks or unmarks the row under the cursor.
func (m *Model) toggleMark() {
	rows := m.markable()
	if len(rows) == 0 {
		m.say("nothing to mark on this tab")
		return
	}
	if m.sel < 0 || m.sel >= len(rows) {
		return
	}
	if m.marked == nil {
		m.marked = map[string]bool{}
	}
	key := rows[m.sel].key
	if m.marked[key] {
		delete(m.marked, key)
	} else {
		m.marked[key] = true
	}
	m.sayMarks()
	// Marking one row and moving on is the common case, so the cursor does it.
	if m.sel < len(rows)-1 {
		m.sel++
	}
}

// markAll marks everything the filter is showing, and clears instead when it
// is all marked already: one key for both halves of the same intent.
func (m *Model) markAll() {
	rows := m.markable()
	if len(rows) == 0 {
		m.say("nothing to mark on this tab")
		return
	}
	all := true
	for _, r := range rows {
		if !m.marked[r.key] {
			all = false
			break
		}
	}
	if all {
		for _, r := range rows {
			delete(m.marked, r.key)
		}
		m.say("marks cleared")
		return
	}
	if m.marked == nil {
		m.marked = map[string]bool{}
	}
	for _, r := range rows {
		m.marked[r.key] = true
	}
	m.sayMarks()
}

func (m *Model) sayMarks() {
	n := len(m.marksHere())
	switch n {
	case 0:
		m.say("no rows marked")
	case 1:
		m.say("1 row marked · y copies it, Y copies the command")
	default:
		m.say(fmt.Sprintf("%d rows marked · y copies them, Y copies the command", n))
	}
}

// yankTargets is what y and Y act on: the marked rows, or the row under the
// cursor when nothing is marked, so the common case is two keys and not three.
func (m Model) yankTargets() []markRow {
	if rows := m.marksHere(); len(rows) > 0 {
		return rows
	}
	rows := m.markable()
	if m.sel >= 0 && m.sel < len(rows) {
		return rows[m.sel : m.sel+1]
	}
	return nil
}

// yankIDs copies the identifiers: pids, pod names or device ids.
func (m *Model) yankIDs() tea.Cmd {
	rows := m.yankTargets()
	if len(rows) == 0 {
		m.say("nothing to copy on this tab")
		return nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	return m.copy(strings.Join(ids, " "))
}

// yankCommand copies the command for the marked rows. siltide does not run it.
func (m *Model) yankCommand() tea.Cmd {
	rows := m.yankTargets()
	if len(rows) == 0 {
		m.say("nothing to copy on this tab")
		return nil
	}
	cmd := markCommand(m.tab, rows)
	if cmd == "" {
		m.say("no command for this tab; y copies the identifiers")
		return nil
	}
	return m.copy(cmd)
}

// markCommand writes the command a tab's rows belong to. Kubernetes rows are
// grouped by namespace, because one kubectl call cannot span two.
func markCommand(tab int, rows []markRow) string {
	switch tab {
	case tabProcesses:
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.id)
		}
		return "kill " + strings.Join(ids, " ")
	case tabKube:
		byNS := map[string][]string{}
		var order []string
		for _, r := range rows {
			if _, seen := byNS[r.ns]; !seen {
				order = append(order, r.ns)
			}
			byNS[r.ns] = append(byNS[r.ns], r.id)
		}
		sort.Strings(order)
		var out []string
		for _, ns := range order {
			line := "kubectl delete pod " + strings.Join(byNS[ns], " ")
			if ns != "" {
				line += " -n " + ns
			}
			out = append(out, line)
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// copyText writes to the terminal's clipboard. It is a variable so a test can
// read what was copied without an escape sequence reaching anything.
var copyText = func(s string) { termenv.Copy(s) }

// copy puts s on the clipboard and says what it copied.
//
// The clipboard is written with OSC 52, which the terminal carries, so it
// works over SSH: these machines are rarely the one in front of you. It is
// also off by default in tmux and in a few terminals, and there is no way to
// ask whether it landed, so the notice repeats the command. A command you can
// read off the screen beats one you have to reconstruct.
func (m *Model) copy(s string) tea.Cmd {
	m.say("copied · " + strings.ReplaceAll(s, "\n", " ; "))
	return func() tea.Msg {
		copyText(s)
		return nil
	}
}

// markedDevices narrows a device list to the marked ones, for the actions
// that work over a set rather than a row. With nothing marked it changes
// nothing, which is what somebody who has never pressed x expects.
func (m Model) markedDevices(devs []device.Device) []device.Device {
	if len(m.marked) == 0 {
		return devs
	}
	var out []device.Device
	for _, d := range devs {
		if m.marked["d:"+d.ID] {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return devs
	}
	return out
}

// markNote is what the tab's summary line says about marks, and nothing at
// all when none are set: a line about a feature nobody is using is noise.
func (m Model) markNote() string {
	n := len(m.marksHere())
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d marked · y copies, Y copies the command · ", n)
}
