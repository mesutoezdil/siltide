package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/provider/sim"
	"github.com/moezdil/siltide/internal/tui"
)

// beat is one move of the demo: keys someone presses, then the frames the
// view is held for. typed is entered one character per frame, the way a
// person fills the filter bar.
type beat struct {
	key   string
	typed string
	hold  int
}

// storyboard is the demo loop: the fleet, a device and its processes, a
// filter typed live, history scrubbed back, then the fabric and health
// views, ending where it started so the loop closes cleanly.
var storyboard = []beat{
	{hold: 12},
	{key: "j", hold: 3}, {key: "j", hold: 10},
	{key: "2", hold: 10},
	{key: "j", hold: 3}, {key: "j", hold: 12},
	{key: "/", hold: 2}, {typed: "util>80", hold: 2}, {key: "enter", hold: 14},
	{key: "esc", hold: 4},
	{key: "3", hold: 12},
	{key: "8", hold: 10},
	{key: ",", hold: 2}, {key: ",", hold: 2}, {key: ",", hold: 8},
	{key: "n", hold: 4}, {key: "m", hold: 10},
	{key: "7", hold: 12},
	{key: "H", hold: 12},
	{key: "D", hold: 14},
	{key: "1", hold: 10},
}

// clockText matches the timestamp in the header, which the film rewrites so
// the clock moves with the simulated fleet rather than with the render.
var clockText = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}`)

// film writes one SVG per frame of the storyboard. The simulated fleet runs
// on a clock of its own, a second per frame, so numbers move between frames
// instead of standing still for the length of the render.
func film(o options, eng *collect.Engine, th tui.Theme, host string) error {
	if err := os.MkdirAll(o.film, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(o.film, "frame-*.svg"))
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return err
		}
	}

	clock := time.Now().Truncate(time.Minute)
	sim.Now = func() time.Time { return clock }
	defer func() { sim.Now = time.Now }()

	var pressed []tea.KeyMsg
	var views []string
	// The fleet steps a second every third frame: fast enough to read as
	// live at 10 frames a second, and it keeps the animation small, since a
	// frame that repeats the one before it costs a delay and nothing else.
	shoot := func() {
		if len(views)%3 == 0 {
			clock = clock.Add(time.Second)
			eng.Collect(context.Background())
		}
		views = append(views, frame(o, eng, th, host, pressed, clock))
	}
	for _, b := range storyboard {
		for _, r := range b.typed {
			pressed = append(pressed, keyMsg(string(r)))
			shoot()
		}
		if b.key != "" {
			pressed = append(pressed, keyMsg(b.key))
		}
		for range max(b.hold, 1) {
			shoot()
		}
	}

	// One window for the whole animation, as tall as the tallest view needs
	// and no taller: a tab that draws less leaves the rest of the terminal
	// dark, the way it does on a screen.
	rows := 0
	for _, v := range views {
		rows = max(rows, len(strings.Split(v, "\n")))
	}
	for i, v := range views {
		name := filepath.Join(o.film, fmt.Sprintf("frame-%04d.svg", i+1))
		if err := os.WriteFile(name, []byte(svg(padRows(v, rows), o.w, rows, "siltide")), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("%s: %d frames of %d columns by %d rows\n", o.film, len(views), o.w, rows)
	if o.player == "" {
		return nil
	}
	return writePlayer(o.player, views, o.w, rows, th)
}

// reel is the animation a page replays as text: the first frame carries
// every line, and each one after it only the lines that changed.
type reel struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	// The terminal's own two colours, so the frame on the page is painted
	// like the capture rather than assumed to be dark.
	Bg     string      `json:"bg"`
	Fg     string      `json:"fg"`
	Frames []reelFrame `json:"frames"`
}

type reelFrame struct {
	Hold  int               `json:"hold,omitempty"` // extra frames this one stands for
	Lines map[string]string `json:"lines"`          // line number to its HTML
}

// writePlayer saves the views as the reel a page plays. Lines repeat between
// frames far more than they change, so only the changes are written.
func writePlayer(path string, views []string, cols, rows int, th tui.Theme) error {
	bg, fg := th.Colors["bg"], th.Colors["text"]
	if bg == "" {
		bg = defaultBG
	}
	if fg == "" {
		fg = defaultFg
	}
	r := reel{Cols: cols, Rows: rows, Bg: bg, Fg: fg}
	var prev []string
	for _, v := range views {
		cur := htmlLines(padRows(v, rows), fg, bg)
		f := reelFrame{Lines: map[string]string{}}
		for i := 0; i < rows; i++ {
			// The first frame carries every line, blank ones included, so a
			// player can loop back to it without clearing the screen first.
			if prev == nil || cur[i] != prev[i] {
				f.Lines[itoa(i)] = cur[i]
			}
		}
		prev = cur
		if len(f.Lines) == 0 && len(r.Frames) > 0 {
			r.Frames[len(r.Frames)-1].Hold++ // nothing moved: hold the last one
			continue
		}
		r.Frames = append(r.Frames, f)
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: %d frames, %.0f KB\n", path, len(r.Frames), float64(len(b))/1024)
	return nil
}

func itoa(i int) string { return fmt.Sprint(i) }

// frame replays the keys pressed so far against the latest snapshot and
// returns the view.
// The model reads its snapshot when it is built, so it is rebuilt per frame:
// that is what lets the numbers move while the keys stay where they were.
func frame(o options, eng *collect.Engine, th tui.Theme, host string, pressed []tea.KeyMsg, clock time.Time) string {
	m := tui.New(eng, tui.Options{Theme: th, Mouse: true, Currency: "$"})
	m = update(m, tea.WindowSizeMsg{Width: o.w, Height: o.h})
	for _, k := range pressed {
		m = update(m, k)
	}
	view := m.View()
	if o.host != "" {
		view = strings.ReplaceAll(view, host, o.host)
	}
	view = dropBadge(view)
	return clockText.ReplaceAllString(view, clock.Format("2006-01-02 15:04:05"))
}

// padRows makes every frame exactly rows tall. A tab whose view is shorter
// would otherwise give the window a different height, and the animation has
// to keep one size from the first frame to the last. The filler is a reset
// escape rather than an empty line, which the renderer would trim off again.
func padRows(view string, rows int) string {
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	for len(lines) < rows {
		lines = append(lines, "\x1b[0m")
	}
	return strings.Join(lines[:rows], "\n")
}

// keyMsg turns a key name into the message Bubble Tea would deliver.
func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	return key(k)
}
