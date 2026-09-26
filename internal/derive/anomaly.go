package derive

import (
	"fmt"
	"math"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/history"
)

// Anomaly is a pattern in a device's history that a single sample cannot
// show: a utilization collapse, a memory leak slope, or temperature creep.
type Anomaly struct {
	Device   string `json:"device"`
	Kind     string `json:"kind"` // "util-collapse", "memory-leak", "temp-creep"
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// Anomalies scans the last window of history for every device.
//
//   - util-collapse: average utilization in the last fifth of the window
//     fell below 30% of the average before it, while the device still has
//     processes (a hung job, a stalled data loader);
//   - memory-leak: used memory rises steadily (fit slope > 0, r² > 0.8) and
//     would fill the device within 4 windows;
//   - temp-creep: temperature rose by more than 10°C across the window with
//     utilization flat (a failing fan or blocked airflow).
func Anomalies(h *history.Store, devs []device.Device, now time.Time, window time.Duration) []Anomaly {
	if h == nil {
		return nil
	}
	var out []Anomaly
	for _, d := range devs {
		pts := h.Between(d.ID, now.Add(-window), now)
		if len(pts) < 10 {
			continue
		}
		util := series(pts, device.Util)
		if len(d.Procs) > 0 {
			cut := len(util) * 4 / 5
			before, after := mean(util[:cut]), mean(util[cut:])
			if !math.IsNaN(before) && !math.IsNaN(after) && before >= 20 && after < before*0.3 {
				out = append(out, Anomaly{d.ID, "util-collapse", fmt.Sprintf("utilization fell from %.0f%% to %.0f%% while %d process(es) hold the device", before, after, len(d.Procs)), "warning"})
			}
		}
		if total, ok := d.Metrics.Get(device.MemTotal); ok && total > 0 {
			slope, r2 := fit(series(pts, device.MemUsed))
			perWindow := slope * float64(len(pts))
			if r2 > 0.8 && perWindow > total*0.02 {
				used := d.Metrics.Or(device.MemUsed, 0)
				if left := total - used; left > 0 && left/perWindow < 4 {
					out = append(out, Anomaly{d.ID, "memory-leak", fmt.Sprintf("memory grows %.1f%% of the device per %s and fills it in about %s", perWindow/total*100, window, (time.Duration(left / perWindow * float64(window))).Truncate(time.Minute)), "warning"})
				}
			}
		}
		temp := series(pts, device.Temp)
		if first, last := mean(head(temp)), mean(tail(temp)); !math.IsNaN(first) && !math.IsNaN(last) && last-first > 10 {
			if uf, ul := mean(head(util)), mean(tail(util)); math.IsNaN(uf) || math.Abs(ul-uf) < 15 {
				out = append(out, Anomaly{d.ID, "temp-creep", fmt.Sprintf("temperature rose from %.0f°C to %.0f°C with utilization flat", first, last), "warning"})
			}
		}
	}
	return out
}

func series(pts []history.Point, k device.Metric) []float64 {
	out := make([]float64, 0, len(pts))
	for _, p := range pts {
		if v, ok := p.Get(k); ok {
			out = append(out, v)
		} else {
			out = append(out, math.NaN())
		}
	}
	return out
}

func head(v []float64) []float64 { return v[:max(len(v)/5, 1)] }
func tail(v []float64) []float64 { return v[len(v)-max(len(v)/5, 1):] }

func mean(v []float64) float64 {
	sum, n := 0.0, 0
	for _, x := range v {
		if !math.IsNaN(x) {
			sum += x
			n++
		}
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

// fit is a least-squares line through the samples (x = index) returning
// the slope per sample and r².
func fit(v []float64) (slope, r2 float64) {
	var n, sx, sy, sxx, sxy, syy float64
	for i, y := range v {
		if math.IsNaN(y) {
			continue
		}
		x := float64(i)
		n++
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
		syy += y * y
	}
	if n < 3 {
		return 0, 0
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, 0
	}
	slope = (n*sxy - sx*sy) / den
	num := n*sxy - sx*sy
	r := num / math.Sqrt(den*(n*syy-sy*sy))
	if math.IsNaN(r) {
		return slope, 0
	}
	return slope, r * r
}
