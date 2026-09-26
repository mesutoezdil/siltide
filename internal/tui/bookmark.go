package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/moezdil/siltide/internal/config"
)

// Bookmark is a view someone returns to: a tab plus the filters that were in
// force when it was saved.
type Bookmark struct {
	Name   string `yaml:"name"`
	Tab    string `yaml:"tab"` // tab name, so the file survives a reordering
	Filter string `yaml:"filter,omitempty"`
	Node   string `yaml:"node,omitempty"`
	NS     string `yaml:"ns,omitempty"`
}

// bookmarkPath is where saved views live, beside history.
func bookmarkPath() string { return filepath.Join(config.StateDir(), "bookmarks.yaml") }

// loadBookmarks reads the saved views. A missing or unreadable file is no
// bookmarks, never an error: they are a convenience, not state to defend.
func loadBookmarks() []Bookmark {
	b, err := os.ReadFile(bookmarkPath())
	if err != nil {
		return nil
	}
	var out []Bookmark
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// writeBookmarks persists the list, sorted so the file reads the same way it
// is listed.
func writeBookmarks(bs []Bookmark) error {
	sort.Slice(bs, func(i, j int) bool { return bs[i].Name < bs[j].Name })
	p := bookmarkPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(bs)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// tabIndex finds a tab by name, or -1.
func tabIndex(name string) int {
	for i, t := range tabs {
		if strings.EqualFold(t.name, name) {
			return i
		}
	}
	return -1
}

// bookmarkNames lists the saved views for completion and for the notice.
func (m Model) bookmarkNames() []string {
	out := make([]string, 0, len(m.marks))
	for _, b := range m.marks {
		out = append(out, b.Name)
	}
	sort.Strings(out)
	return out
}

// bookmark runs ":bookmark": no argument lists, "save <name>" records the
// current view, "rm <name>" forgets one, and a bare name opens it.
func (m *Model) bookmark(arg string) {
	f := strings.Fields(arg)
	switch {
	case len(f) == 0:
		if len(m.marks) == 0 {
			m.say("no bookmarks yet: :bookmark save <name>")
			return
		}
		m.say("bookmarks: " + strings.Join(m.bookmarkNames(), ", "))
	case (f[0] == "save" || f[0] == "add") && len(f) > 1:
		m.saveBookmark(f[1])
	case (f[0] == "rm" || f[0] == "delete") && len(f) > 1:
		m.removeBookmark(f[1])
	case f[0] == "save" || f[0] == "rm" || f[0] == "add" || f[0] == "delete":
		m.say("bookmark " + f[0] + " <name>")
	default:
		m.openBookmark(f[0])
	}
}

// saveBookmark records the current tab and filters under name, replacing a
// bookmark of the same name.
func (m *Model) saveBookmark(name string) {
	b := Bookmark{Name: name, Tab: tabs[m.tab].name, Filter: m.search, Node: m.node, NS: m.ns}
	replaced := false
	for i := range m.marks {
		if strings.EqualFold(m.marks[i].Name, name) {
			m.marks[i], replaced = b, true
			break
		}
	}
	if !replaced {
		m.marks = append(m.marks, b)
	}
	if err := writeBookmarks(m.marks); err != nil {
		m.say("bookmark: " + err.Error())
		return
	}
	m.say("saved " + name + " (" + strings.ToLower(b.Tab) + describeFilters(b) + ")")
}

// removeBookmark forgets one saved view.
func (m *Model) removeBookmark(name string) {
	for i, b := range m.marks {
		if strings.EqualFold(b.Name, name) {
			m.marks = append(m.marks[:i], m.marks[i+1:]...)
			if err := writeBookmarks(m.marks); err != nil {
				m.say("bookmark: " + err.Error())
				return
			}
			m.say("removed " + b.Name)
			return
		}
	}
	m.unknownBookmark(name)
}

// openBookmark restores a saved view.
func (m *Model) openBookmark(name string) {
	for _, b := range m.marks {
		if !strings.EqualFold(b.Name, name) {
			continue
		}
		if i := tabIndex(b.Tab); i >= 0 {
			m.setTab(i)
		}
		m.setSearch(b.Filter)
		m.node, m.ns = b.Node, b.NS
		m.say(b.Name + ": " + strings.ToLower(b.Tab) + describeFilters(b))
		return
	}
	m.unknownBookmark(name)
}

func (m *Model) unknownBookmark(name string) {
	if len(m.marks) == 0 {
		m.say("no bookmark " + name + "; :bookmark save <name> records the current view")
		return
	}
	m.say("no bookmark " + name + "; have " + strings.Join(m.bookmarkNames(), ", "))
}

// describeFilters is the ", filter x, node y" tail of a bookmark notice.
func describeFilters(b Bookmark) string {
	var parts []string
	for _, p := range []struct{ label, value string }{{"filter", b.Filter}, {"node", b.Node}, {"ns", b.NS}} {
		if p.value != "" {
			parts = append(parts, p.label+" "+p.value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}
