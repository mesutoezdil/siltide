// Command shots renders terminal UI (TUI) tabs to the SVG images under
// `assets/`. By default it drives the simulated fleet (synthetic numbers,
// the "(simulated)" suffix dropped for room); with -live it runs the real
// providers on this machine for a while first and captures those.
//
//	go run ./scripts/shots -out assets
//	go run ./scripts/shots -live 90s -host m4-pro -tabs overview,devices -prefix mac- -out assets
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/history"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/provider/apple"
	"github.com/moezdil/siltide/internal/provider/sim"
	"github.com/moezdil/siltide/internal/tui"
)

func main() {
	out := flag.String("out", "assets", "directory for the .svg files")
	width := flag.Int("width", 160, "columns")
	height := flag.Int("height", 42, "rows")
	theme := flag.String("theme", "default", "theme name")
	ans := flag.String("ans", "", "also write the raw ANSI views to this directory")
	live := flag.Duration("live", 0, "capture the real providers on this machine after collecting this long (0: the demo fleet)")
	hostLabel := flag.String("host", "", "hostname to show (default: h100-node-07 for the demo, the real one live)")
	tabsOnly := flag.String("tabs", "", "comma-separated tab names to render (default: all)")
	prefix := flag.String("prefix", "", "file name prefix, for example mac-")
	filmDir := flag.String("film", "", "write the demo animation frames (one SVG each) to this directory instead of the tab images")
	player := flag.String("player", "", "with -film, also write the animation as HTML lines a page can replay, to this file")
	flag.Parse()
	if err := run(options{out: *out, ans: *ans, w: *width, h: *height, theme: *theme, live: *live, host: *hostLabel, tabs: *tabsOnly, prefix: *prefix, film: *filmDir, player: *player}); err != nil {
		fmt.Fprintln(os.Stderr, "shots:", err)
		os.Exit(1)
	}
}

type options struct {
	out, ans, theme, host, tabs, prefix string
	film, player                        string
	w, h                                int
	live                                time.Duration
}

func run(o options) error {
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	// No terminal is attached, so lipgloss would strip every color.
	lipgloss.SetColorProfile(termenv.TrueColor)
	cfg := config.Default()
	cfg.Cost.PerHour = map[string]float64{"H100": 3.5, "MI300X": 3, "910B": 1.8}
	cfg.Carbon.GramsPerKWh = 400
	hist, err := history.Open(history.Options{Keep: cfg.History.Keep, Resolution: cfg.History.Resolution})
	if err != nil {
		return err
	}
	ctx := context.Background()
	var eng *collect.Engine
	if o.live > 0 {
		eng = collect.New([]provider.Provider{apple.Provider()}, cfg, hist, false)
		defer eng.Close()
		eng.Detect()
		for end := time.Now().Add(o.live); time.Now().Before(end); time.Sleep(time.Second) {
			eng.Collect(ctx)
		}
	} else {
		// 30 minutes of history so the History and Dashboard tabs have
		// curves: the fleet moves on a clock we advance by hand.
		now := time.Now()
		clock := now.Add(-30 * time.Minute)
		sim.Now = func() time.Time { return clock }
		sim.Suffix = "" // the DEMO badge in the header marks the data
		prov := sim.Provider(8)
		for ; clock.Before(now); clock = clock.Add(cfg.History.Resolution) {
			devs, err := prov.Read(ctx)
			if err != nil {
				return err
			}
			hist.Record(clock, devs)
		}
		sim.Now = time.Now
		eng = collect.New([]provider.Provider{prov}, cfg, hist, true)
		defer eng.Close()
		eng.Detect()
		for range 3 {
			eng.Collect(ctx)
		}
		if o.host == "" {
			o.host = "h100-node-07"
		}
	}
	th, _ := tui.LoadTheme(o.theme, "", nil, false)
	host, _ := os.Hostname()
	if o.film != "" {
		return film(o, eng, th, host)
	}
	m := tui.New(eng, tui.Options{Theme: th, Mouse: true, Currency: "$"})
	m = update(m, tea.WindowSizeMsg{Width: o.w, Height: o.h})
	for _, tab := range tui.TabKeys() {
		name := strings.ToLower(tab.Name)
		if o.tabs != "" && !slices.Contains(strings.Split(o.tabs, ","), name) {
			continue
		}
		m = update(m, key(tab.Key))
		view := m.View()
		if o.host != "" {
			view = strings.ReplaceAll(view, host, o.host)
		}
		if o.live == 0 {
			view = dropBadge(view)
		}
		bg, fg := th.Colors["bg"], th.Colors["text"]
		if bg == "" {
			bg = defaultBG
		}
		if fg == "" || !strings.HasPrefix(fg, "#") {
			fg = defaultFg // an ANSI index means "whatever the terminal uses"
		}
		if err := os.WriteFile(filepath.Join(o.out, o.prefix+name+".svg"), []byte(svgOn(view, o.w, o.h, "siltide · "+tab.Name, bg, fg)), 0o644); err != nil {
			return err
		}
		if o.ans != "" {
			if err := os.WriteFile(filepath.Join(o.ans, o.prefix+name+".ans"), []byte(view), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// demoBadge matches the header's DEMO segment with its separator.
var demoBadge = regexp.MustCompile("\x1b\\[[0-9;]*m  │  \x1b\\[0m\x1b\\[[0-9;]*mDEMO: simulated data\x1b\\[0m")

// dropBadge removes the DEMO badge from the header and keeps the clock
// right-aligned; README.md says where the pictures come from.
func dropBadge(view string) string {
	lines := strings.SplitN(view, "\n", 2)
	head := demoBadge.ReplaceAllString(lines[0], "")
	if cut := width(lines[0]) - width(head); cut > 0 {
		if i := strings.LastIndex(head, "\x1b["); i >= 0 {
			head = head[:i] + strings.Repeat(" ", cut) + head[i:]
		}
	}
	if len(lines) == 1 {
		return head
	}
	return head + "\n" + lines[1]
}

// width counts visible cells: the views use no wide runes.
func width(s string) int {
	return len([]rune(regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(s, "")))
}

func key(k string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)} }

func update(m tui.Model, msg tea.Msg) tui.Model {
	next, _ := m.Update(msg)
	return next.(tui.Model)
}
