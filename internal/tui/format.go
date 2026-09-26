package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/moezdil/siltide/internal/device"
)

const na = "N/A"

// bytes formats a byte count in the largest fitting unit.
func bytes(b float64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.2fT", b/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", b/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", b/(1<<20))
	default:
		return fmt.Sprintf("%.0fK", b/(1<<10))
	}
}

// rate formats bytes per second.
func rate(b float64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1fGB/s", b/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.0fMB/s", b/1e6)
	default:
		return fmt.Sprintf("%.0fKB/s", b/1e3)
	}
}

// memText is "used/total" or N/A.
func memText(m device.Metrics) string {
	used, ok1 := m.Get(device.MemUsed)
	total, ok2 := m.Get(device.MemTotal)
	switch {
	case ok1 && ok2:
		return bytes(used) + "/" + bytes(total)
	case ok2:
		return "?/" + bytes(total)
	case ok1:
		return bytes(used)
	default:
		return na
	}
}

// metricText formats one metric with its unit.
func metricText(k device.Metric, v float64) string {
	switch k {
	case device.Util, device.Fan, device.MemBandwidth, device.Encoder, device.Decoder:
		return fmt.Sprintf("%.0f%%", v)
	case device.MemUsed, device.MemTotal:
		return bytes(v)
	case device.Temp:
		return fmt.Sprintf("%.0f°C", v)
	case device.Power, device.PowerCap:
		return fmt.Sprintf("%.0fW", v)
	case device.ClockCore, device.ClockMem:
		return fmt.Sprintf("%.0fMHz", v)
	case device.PCIeRx, device.PCIeTx:
		return rate(v)
	case device.PCIeGen, device.PCIeMaxGen:
		return fmt.Sprintf("gen%.0f", v)
	case device.PCIeWidth, device.PCIeMaxWidth:
		return fmt.Sprintf("x%.0f", v)
	case device.Throttle:
		if names := device.ThrottleNames(v); len(names) > 0 {
			return strings.Join(names, ", ")
		}
		return "none"
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// opt formats a metric or a dimmed N/A.
func (t Theme) opt(m device.Metrics, k device.Metric) string {
	if v, ok := m.Get(k); ok {
		return metricText(k, v)
	}
	return t.dim.Render(na)
}

// bar draws a percentage bar of width w; negative p draws an empty track.
func (t Theme) bar(p float64, w int) string {
	if w <= 0 {
		return ""
	}
	if p < 0 || math.IsNaN(p) {
		return t.role("bar_track").Render(strings.Repeat("·", w))
	}
	n := min(max(int(math.Round(p/100*float64(w))), 0), w)
	return t.level(p).Render(strings.Repeat("█", n)) + t.role("bar_track").Render(strings.Repeat("░", w-n))
}

// pctBar is a bar followed by its percentage.
func (t Theme) pctBar(p float64, ok bool, w int) string {
	if !ok {
		return t.bar(-1, w) + " " + rpad(t.dim.Render(na), 4)
	}
	return t.bar(p, w) + " " + rpad(fmt.Sprintf("%.0f%%", p), 4)
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// spark draws samples scaled to max; NaN samples leave a gap. When more
// samples than width are given, the newest fill the width.
func spark(vals []float64, max float64, width int) string {
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	var b strings.Builder
	for _, v := range vals {
		if math.IsNaN(v) || max <= 0 {
			b.WriteByte(' ')
			continue
		}
		i := int(v / max * float64(len(sparks)-1))
		b.WriteRune(sparks[min(max0(i), len(sparks)-1)])
	}
	return b.String()
}

// resample shrinks vals to width points by averaging buckets.
func resample(vals []float64, width int) []float64 {
	if width <= 0 || len(vals) <= width {
		return vals
	}
	out := make([]float64, width)
	for i := range out {
		lo, hi := i*len(vals)/width, (i+1)*len(vals)/width
		sum, n := 0.0, 0
		for _, v := range vals[lo:max(hi, lo+1)] {
			if !math.IsNaN(v) {
				sum += v
				n++
			}
		}
		out[i] = math.NaN()
		if n > 0 {
			out[i] = sum / float64(n)
		}
	}
	return out
}

func max0(i int) int { return max(i, 0) }

func peak(vals []float64) float64 {
	p := 0.0
	for _, v := range vals {
		if v > p {
			p = v
		}
	}
	return p
}

// pad right-pads (or truncates) s to width w, measured in cells.
func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return trunc(s, w)
}

// rpad left-pads s to width w.
// labelWidth is the widest device label in devs, at least 6, so node-prefixed
// labels such as "ascend-01:0" keep the columns after them aligned.
func labelWidth(devs []device.Device) int {
	w := 6
	for _, d := range devs {
		w = max(w, min(len(d.Label()), 14))
	}
	return w
}

func rpad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// reset ends every style lipgloss writes.
const reset = "\x1b[0m"

// selected marks the row under the cursor. The row carries the escapes of
// its own cells, and the reset closing the first coloured one would drop the
// selection's colour for the rest of the line, leaving the highlight cut off
// partway across, so the opening sequence goes back in after every reset.
func selected(style lipgloss.Style, line string) string {
	marked := style.Render("\x00")
	open, closing, ok := strings.Cut(marked, "\x00")
	if !ok || open == "" {
		return line // no colour in this profile: nothing to keep alive
	}
	return open + strings.ReplaceAll(line, reset, reset+open) + closing
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	// Cutting from the end takes the closing escape with it, and a colour
	// left open runs on into whatever is drawn next, so it goes back on.
	out := string(r) + "…"
	if strings.Contains(s, "\x1b[") && !strings.HasSuffix(out, reset) {
		out += reset
	}
	return out
}

func width(s string) int { return lipgloss.Width(s) }

// column describes one table column.
type column struct {
	name  string
	w     int
	right bool
}

// table renders a header and rows; sel highlights a row (-1 for none).
// Rows scroll so the selection stays within height lines. sortCol marks
// the sorted column in the header (-1 for none).
func (t Theme) table(cols []column, rows [][]string, sel, height, width, sortCol int, desc bool) string {
	return t.tableMarks(cols, rows, sel, height, width, sortCol, desc, nil)
}

// tableMarks is table, with a set of row indices drawn as marked. A marked row
// takes the accent across its whole line, which reads next to the cursor row
// rather than fighting it: a row can be both, and then it is both.
func (t Theme) tableMarks(cols []column, rows [][]string, sel, height, width, sortCol int, desc bool, marked map[int]bool) string {
	var b strings.Builder
	cell := func(c column, s string) string {
		if c.right {
			return rpad(trunc(s, c.w), c.w)
		}
		return pad(s, c.w)
	}
	var head []string
	for i, c := range cols {
		name := c.name
		if i == sortCol && name != "" {
			if desc {
				name += "▾"
			} else {
				name += "▴"
			}
		}
		head = append(head, cell(c, name))
	}
	b.WriteString(t.dim.Render(strings.Join(head, " ")) + "\n")
	start := 0
	if height > 0 && sel >= height {
		start = sel - height + 1
	}
	end := len(rows)
	if height > 0 && end-start > height {
		end = start + height
	}
	for i := start; i < end; i++ {
		var cells []string
		for j, c := range cols {
			v := ""
			if j < len(rows[i]) {
				v = rows[i][j]
			}
			cells = append(cells, cell(c, v))
		}
		line := strings.Join(cells, " ")
		if marked[i] {
			line = selected(t.accent, line)
		}
		if i == sel {
			line = selected(t.sel, pad(line, max(width, lipgloss.Width(line))))
		}
		b.WriteString(line + "\n")
	}
	if end < len(rows) {
		b.WriteString(t.dim.Render(fmt.Sprintf("… %d more", len(rows)-end)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
