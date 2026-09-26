// Package health scores a device from 0 to 100 using only what its vendor
// reports. The score is siltide's opinion, not a vendor metric: every
// deduction comes with a note that says why, and missing data never lowers
// the score.
//
// Deductions: uncorrected ECC (error-correcting code) errors 30, row remap
// failed 30, hardware slowdown 25, driver-recommended recovery (Xid 154)
// 25, a critical Xid in the last 10 minutes 20 (a warning Xid 10), thermal
// throttling 15, remap pending 10, over the warning temperature 10,
// power-cap throttling 10, PCIe link narrower than the maximum 10, PCIe
// link slower than the maximum 10, interconnect links down 10, interconnect
// errors 5, no metrics at all 50.
//
// Bands: 90+ healthy, 75+ good, 50+ degraded, 25+ unhealthy, below 25
// critical.
package health

import (
	"fmt"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Options are the inputs beyond the device itself.
type Options struct {
	TempWarn float64
	Xids     []int  // raised within the recent window
	XidWorst string // "critical", "warning", "" for none
}

// Score returns the score and its notes.
func Score(d device.Device, o Options) (int, []string) {
	score := 100
	var notes []string
	take := func(n int, note string) {
		score -= n
		notes = append(notes, note)
	}
	if len(d.Metrics) == 0 {
		take(50, "no metrics reported")
	}
	if v := d.Metrics.Or(device.EccUncorrected, 0); v > 0 {
		take(30, fmt.Sprintf("%.0f uncorrected ECC errors", v))
	}
	if d.Metrics.Or(device.RemapFailed, 0) > 0 {
		take(30, "row remapping failed")
	}
	bits := int(d.Metrics.Or(device.Throttle, 0))
	if bits&device.ThrottleHW != 0 {
		take(25, "hardware slowdown")
	}
	for _, x := range o.Xids {
		if x == 154 {
			take(25, "driver recommends a recovery action (Xid 154)")
			break
		}
	}
	switch o.XidWorst {
	case "critical":
		take(20, "critical Xid in the last 10 minutes: "+list(o.Xids))
	case "warning":
		take(10, "Xid in the last 10 minutes: "+list(o.Xids))
	}
	if bits&device.ThrottleThermal != 0 {
		take(15, "thermal throttling")
	}
	if d.Metrics.Or(device.RemapPending, 0) > 0 {
		take(10, "row remap pending a reset")
	}
	if bits&device.ThrottlePowerCap != 0 {
		take(10, "held at the power cap")
	}
	if t, ok := d.Metrics.Get(device.Temp); ok && t >= o.TempWarn {
		take(10, fmt.Sprintf("%.0f°C, warning at %.0f°C", t, o.TempWarn))
	}
	w, ok1 := d.Metrics.Get(device.PCIeWidth)
	mw, ok2 := d.Metrics.Get(device.PCIeMaxWidth)
	if ok1 && ok2 && w < mw {
		take(10, fmt.Sprintf("PCIe link x%.0f of x%.0f", w, mw))
	}
	g, ok1 := d.Metrics.Get(device.PCIeGen)
	mg, ok2 := d.Metrics.Get(device.PCIeMaxGen)
	if ok1 && ok2 && g < mg {
		take(10, fmt.Sprintf("PCIe gen %.0f of %.0f", g, mg))
	}
	la, ok1 := d.Metrics.Get(device.LinksActive)
	lt, ok2 := d.Metrics.Get(device.LinksTotal)
	if ok1 && ok2 && la < lt {
		take(10, fmt.Sprintf("%.0f of %.0f interconnect links down", lt-la, lt))
	}
	var errs uint64
	for _, l := range d.Links {
		errs += l.Errors
	}
	if errs > 0 {
		take(5, fmt.Sprintf("%d interconnect link errors", errs))
	}
	return max(score, 0), notes
}

// Band names the score range.
func Band(score int) string {
	switch {
	case score >= 90:
		return "healthy"
	case score >= 75:
		return "good"
	case score >= 50:
		return "degraded"
	case score >= 25:
		return "unhealthy"
	}
	return "critical"
}

func list(codes []int) string {
	var s []string
	for _, c := range codes {
		s = append(s, fmt.Sprint(c))
	}
	return strings.Join(s, ", ")
}
