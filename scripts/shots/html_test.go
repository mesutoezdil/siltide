package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moezdil/siltide/internal/tui"
)

func TestHTMLLines(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain", "plain"},
		{"a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"\x1b[31mred\x1b[0m", `<span style="color:#ff7b72">red</span>`},
		{"\x1b[1mbold\x1b[0m", `<span style="font-weight:700">bold</span>`},
		{"\x1b[38;2;18;52;86mtrue\x1b[0m", `<span style="color:#123456">true</span>`},
		{"\x1b[2mfaint\x1b[0m", `<span style="opacity:.7">faint</span>`},
		{"", ""},
		{"\x1b[0m", ""},
		{"\x1b[7mrev\x1b[0m", `<span style="color:#0d1117;background:#c9d1d9">rev</span>`},
	} {
		got := htmlLines(c.in, defaultFg, defaultBG)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("htmlLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// one line per line of the view, blank ones included
	if got := htmlLines("a\n\nb\n", defaultFg, defaultBG); len(got) != 3 || got[1] != "" {
		t.Errorf("htmlLines over three lines gave %q", got)
	}
}

func TestWritePlayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.json")
	views := []string{
		"one\ntwo\nthree",
		"one\ntwo\nthree", // nothing moved
		"one\nTWO\nthree", // one line changed
	}
	if err := writePlayer(path, views, 40, 3, tui.NewTheme("default", nil)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r reel
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}

	if r.Cols != 40 || r.Rows != 3 {
		t.Errorf("reel is %dx%d, want 40x3", r.Cols, r.Rows)
	}
	if len(r.Frames) != 2 {
		t.Fatalf("wrote %d frames for three views with one repeat, want 2", len(r.Frames))
	}
	if len(r.Frames[0].Lines) != 3 {
		t.Errorf("the first frame carries %d lines, want all 3 so a loop needs no clearing", len(r.Frames[0].Lines))
	}
	if r.Frames[0].Hold != 1 {
		t.Errorf("the repeated view should hold: hold %d, want 1", r.Frames[0].Hold)
	}
	if len(r.Frames[1].Lines) != 1 || r.Frames[1].Lines["1"] != "TWO" {
		t.Errorf("the second frame should carry only the line that changed: %v", r.Frames[1].Lines)
	}
}

func TestWritePlayerPadsShortViews(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.json")
	if err := writePlayer(path, []string{"just one line"}, 20, 5, tui.NewTheme("default", nil)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var r reel
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Frames[0].Lines) != 5 {
		t.Fatalf("a short view should still fill the %d rows: %v", r.Rows, r.Frames[0].Lines)
	}
	if strings.Contains(string(b), "\x1b") {
		t.Error("an escape sequence reached the page")
	}
}

// A light capture reverses to its own paper. Reading a reversed cell as dark
// text is what made the recording on the light page hard to read.
func TestHTMLLinesReverseTakesTheTerminalsOwnBackground(t *testing.T) {
	const paperBG, paperFG = "#f7f5ef", "#2b2b2b"
	got := htmlLines("\x1b[7;38;2;11;92;173mDevices\x1b[0m", paperFG, paperBG)
	if len(got) != 1 || !strings.Contains(got[0], "color:"+paperBG) {
		t.Fatalf("reversed cell rendered as %q, want its text in %s", got, paperBG)
	}
	if strings.Contains(got[0], defaultBG) {
		t.Fatalf("reversed cell fell back to the dark page background: %q", got[0])
	}
}

// Faint has to mean something different on paper: there the dim colour is
// already close to the background, and a dark terminal's fade leaves the text
// under three to one against the page.
func TestFaintFadesLessOnALightTerminal(t *testing.T) {
	dark := htmlLines("\x1b[2mdim\x1b[0m", defaultFg, defaultBG)
	light := htmlLines("\x1b[2mdim\x1b[0m", "#2b2b2b", "#f7f5ef")
	if !strings.Contains(dark[0], "opacity:.7") {
		t.Fatalf("dark: %q", dark[0])
	}
	if !strings.Contains(light[0], "opacity:.86") {
		t.Fatalf("light: %q", light[0])
	}
}

// A cell with no colour of its own takes the terminal's, whatever that is.
// The renderer used to answer #c9d1d9 for every such cell, which is the dark
// theme's grey: on a paper capture it put the ordinary text of the interface
// in near-white on cream, and the page carrying it was unreadable.
func TestPlainTextTakesTheTerminalsForeground(t *testing.T) {
	const paperBG, paperFG = "#f7f5ef", "#2b2b2b"
	got := htmlLines("\x1b[38;2;11;92;173msiltide\x1b[0m  h100-node-07", paperFG, paperBG)
	if strings.Contains(got[0], defaultFg) {
		t.Fatalf("a dark grey reached a paper capture: %q", got[0])
	}
	if strings.Contains(got[0], "<span") && strings.Count(got[0], "<span") != 1 {
		t.Fatalf("plain text should carry no colour of its own: %q", got[0])
	}
}
