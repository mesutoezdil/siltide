package tui

import (
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/moezdil/siltide/internal/config"
)

// session is where the interface was when it was last closed. It is the same
// four things a bookmark holds, saved without being asked, so starting
// siltide again lands where the last hour was spent rather than on Overview.
type session struct {
	Tab      string `yaml:"tab"`
	Filter   string `yaml:"filter,omitempty"`
	Node     string `yaml:"node,omitempty"`
	NS       string `yaml:"ns,omitempty"`
	SortCol  int    `yaml:"sort_col,omitempty"`
	SortDesc bool   `yaml:"sort_desc,omitempty"`
}

// sessionPath is where it is kept, beside the bookmarks.
func sessionPath() string { return filepath.Join(config.StateDir(), "session.yaml") }

// loadSession reads the last view. Anything unreadable is no session: this is
// a convenience, and a bad file should cost a default start, not an error.
func loadSession() (session, bool) {
	b, err := os.ReadFile(sessionPath())
	if err != nil {
		return session{}, false
	}
	var s session
	if err := yaml.Unmarshal(b, &s); err != nil || s.Tab == "" {
		return session{}, false
	}
	return s, true
}

// saveSession records the current view. Called when the interface closes.
func (m Model) saveSession() error {
	s := session{Tab: tabs[m.tab].name, Filter: m.search, Node: m.node, NS: m.ns, SortCol: m.sortCol, SortDesc: m.sortDesc}
	b, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(sessionPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(sessionPath(), b, 0o644)
}

// restore puts the interface back where it was, or where the command line
// asked for. Help is never restored: nobody means to start there.
func (m *Model) restore(s session) {
	if i := tabIndex(s.Tab); i >= 0 && i != tabHelp {
		m.setTab(i)
	}
	m.setSearch(s.Filter)
	m.node, m.ns = s.Node, s.NS
	m.sortCol, m.sortDesc = s.SortCol, s.SortDesc
}

// openView is what --tab, --filter, --node, --ns and --bookmark ask for.
// A bookmark names a saved view; the rest override whatever it carried, so
// `--bookmark incident --tab events` opens that view on another tab.
type openView struct {
	Bookmark string
	Tab      string
	Filter   string
	Node     string
	NS       string
}

// Empty reports whether the command line asked for nothing.
func (o openView) Empty() bool {
	return o.Bookmark == "" && o.Tab == "" && o.Filter == "" && o.Node == "" && o.NS == ""
}

// apply opens what the command line asked for and says what it could not.
func (m *Model) openFromFlags(o openView) {
	if o.Bookmark != "" {
		m.openBookmark(o.Bookmark)
	}
	if o.Tab != "" {
		i := tabIndex(o.Tab)
		if i < 0 {
			i = tabIndexPrefix(o.Tab)
		}
		if i < 0 {
			m.say("no tab called " + o.Tab + "; have " + strings.Join(tabNames(), ", "))
		} else {
			m.setTab(i)
		}
	}
	if o.Filter != "" {
		m.setSearch(o.Filter)
	}
	if o.Node != "" {
		m.node = o.Node
	}
	if o.NS != "" {
		m.ns = o.NS
	}
}

// tabIndexPrefix matches a tab by the start of its name, the way the command
// bar does, so --tab proc finds Processes.
func tabIndexPrefix(name string) int {
	name = strings.ToLower(name)
	for i, t := range tabs {
		if strings.HasPrefix(strings.ToLower(t.name), name) {
			return i
		}
	}
	return -1
}

// tabNames lists the tabs for an error message.
func tabNames() []string {
	out := make([]string, len(tabs))
	for i, t := range tabs {
		out[i] = strings.ToLower(t.name)
	}
	return out
}

// OpenView builds what the command line asked for, so main does not need to
// know the shape of it.
func OpenView(bookmark, tab, filter, node, ns string) openView {
	return openView{Bookmark: bookmark, Tab: tab, Filter: filter, Node: node, NS: ns}
}
