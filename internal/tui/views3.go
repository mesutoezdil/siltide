package tui

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/history"
)

// topologyMatrix renders the device-to-device connection levels of one
// node, `nvidia-smi topo` style, and placement hints: which free devices
// share the closest domain. NUMA in the legend is non-uniform memory access.
func (m Model) topologyMatrix(devs []device.Device) string {
	th := m.th
	var withTopo []device.Device
	for _, d := range devs {
		if len(d.Topology) > 0 && d.Parent == "" {
			withTopo = append(withTopo, d)
		}
	}
	if len(withTopo) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n" + th.bold.Render("Topology") + th.dim.Render("  NV<n> NVLink, PIX one switch, PXB switches, PHB host bridge, NODE same NUMA, SYS across NUMA") + "\n")
	head := "      "
	for _, d := range withTopo {
		head += fmt.Sprintf("%5s ", d.Label())
	}
	b.WriteString(th.dim.Render(head) + "\n")
	for _, d := range withTopo {
		row := fmt.Sprintf("%-6s", d.Label())
		for _, peer := range withTopo {
			cell := "  X  "
			if peer.ID != d.ID {
				cell = fmt.Sprintf("%5s", d.Topology[peer.Index])
				if strings.HasPrefix(cell[1:], "NV") || strings.Contains(cell, "NV") {
					cell = th.ok.Render(cell)
				} else if strings.Contains(cell, "SYS") {
					cell = th.dim.Render(cell)
				}
			}
			row += cell + " "
		}
		b.WriteString(row + "\n")
	}
	// placement hints: free devices grouped by their tightest domain
	var free []device.Device
	for _, d := range withTopo {
		if len(d.Procs) == 0 && d.Allocated == "" {
			free = append(free, d)
		}
	}
	if len(free) >= 2 {
		groups := map[string][]string{}
		for i, a := range free {
			for _, c := range free[i+1:] {
				lvl := a.Topology[c.Index]
				groups[lvl] = append(groups[lvl], a.Label()+"+"+c.Label())
			}
		}
		order := []string{"NV", "PIX", "PXB", "PHB", "NODE", "SYS"}
		var best string
		for _, o := range order {
			for lvl := range groups {
				if strings.HasPrefix(lvl, o) {
					best = lvl
					break
				}
			}
			if best != "" {
				break
			}
		}
		if best != "" {
			pairs := groups[best]
			sort.Strings(pairs)
			fmt.Fprintf(&b, "%s %d free devices; closest pairs over %s: %s\n", th.info.Render("placement"), len(free), best, strings.Join(pairs, " "))
		}
	}
	return b.String()
}

// reliabilityTable is the dashboard's per-device error and counter table.
func (m Model) reliabilityTable(devs []device.Device) string {
	th := m.th
	cols := []column{{"#", 6, false}, {"XID 10m", 8, true}, {"ECC C/U", 9, true}, {"REMAP", 6, true}, {"RETIRED", 7, true}, {"REPLAYS", 7, true}, {"AER C/F", 9, true}, {"VIOL P/T", 10, true}, {"ENERGY", 9, true}, {"LINK ERR", 8, true}, {"HEALTH", 6, true}}
	var rows [][]string
	for _, d := range devs {
		if d.Parent != "" {
			continue
		}
		xids := 0
		for _, e := range m.eng.Events() {
			if e.Kind == "xid" && e.Device == d.ID && m.snap.Time.Sub(e.Time) <= 10*60e9 {
				xids++
			}
		}
		xidS := fmt.Sprint(xids)
		if xids > 0 {
			xidS = th.warn.Render(xidS)
		}
		ecc := th.dim.Render(na)
		if c, ok := d.Metrics.Get(device.EccCorrected); ok {
			u := d.Metrics.Or(device.EccUncorrected, 0)
			ecc = fmt.Sprintf("%.0f/%.0f", c, u)
			if u > 0 {
				ecc = th.warn.Render(ecc)
			}
		}
		aer := th.dim.Render(na)
		if c, ok := d.Metrics.Get(device.AERCorrected); ok {
			aer = fmt.Sprintf("%.0f/%.0f", c, d.Metrics.Or(device.AERFatal, 0))
		}
		viol := th.dim.Render(na)
		if p, ok := d.Metrics.Get(device.ViolationPower); ok {
			viol = fmt.Sprintf("%.0f%%/%.0f%%", p, d.Metrics.Or(device.ViolationTherm, 0))
		}
		energy := th.dim.Render(na)
		if en, ok := m.snap.Energy[d.ID]; ok {
			energy = fmt.Sprintf("%.2fkWh", en.KWh)
		}
		var errs uint64
		for _, l := range d.Links {
			errs += l.Errors
		}
		lerr := th.dim.Render(na)
		if len(d.Links) > 0 {
			lerr = fmt.Sprint(errs)
		}
		rows = append(rows, []string{d.Label(), xidS, ecc, th.opt(d.Metrics, device.RemappedRows), th.opt(d.Metrics, device.RetiredPages), th.opt(d.Metrics, device.PCIeReplays), aer, viol, energy, lerr, m.health(d)})
	}
	if len(rows) == 0 {
		return ""
	}
	return th.bold.Render("Reliability") + "\n" + th.table(cols, rows, -1, 0, m.width, -1, false)
}

// ExportCSV writes the tracked history of devs as rows of
// `time,device,name,metric,value`. It returns the row count.
func ExportCSV(w io.Writer, h *history.Store, devs []device.Device) (int, error) {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"time", "device", "name", "metric", "value"}); err != nil {
		return 0, err
	}
	n := 0
	for _, d := range devs {
		for _, p := range h.Series(d.ID) {
			for _, k := range history.Tracked {
				if v, ok := p.Get(k); ok {
					if err := cw.Write([]string{p.T.UTC().Format("2006-01-02T15:04:05Z"), d.ID, d.Name, string(k), strconv.FormatFloat(v, 'g', -1, 64)}); err != nil {
						return n, err
					}
					n++
				}
			}
		}
	}
	cw.Flush()
	return n, cw.Error()
}

func createFile(path string) (*os.File, error) { return os.Create(path) }
