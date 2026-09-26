// Package cost turns device-hours into money using prices from the config,
// so waste has a number attached.
package cost

import (
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Table maps a device name substring to a price per device-hour.
type Table struct {
	Currency string
	PerHour  map[string]float64
}

// Price returns the hourly price of a device, or 0 when unpriced. The
// longest matching substring wins, so "H100 80GB" beats "H100".
func (t Table) Price(d device.Device) float64 {
	best, bestLen := 0.0, -1
	for k, v := range t.PerHour {
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(k)) && len(k) > bestLen {
			best, bestLen = v, len(k)
		}
	}
	return best
}

// Summary is spend and waste per hour for a set of devices.
type Summary struct {
	SpendPerHour float64 `json:"spend_per_hour"` // sum of prices of every priced device
	WastePerHour float64 `json:"waste_per_hour"` // price times unused share on allocated devices
	IdlePerHour  float64 `json:"idle_per_hour"`  // price of devices with no work at all
	Priced       int     `json:"priced"`
}

// Summarize prices a set of devices.
func (t Table) Summarize(devs []device.Device) Summary {
	var s Summary
	for _, d := range devs {
		p := t.Price(d)
		if p == 0 || d.Parent != "" {
			continue
		}
		s.Priced++
		s.SpendPerHour += p
		util, ok := d.Metrics.Get(device.Util)
		switch {
		case len(d.Procs) > 0 && ok:
			s.WastePerHour += p * (100 - util) / 100
		case len(d.Procs) == 0 && d.Allocated == "":
			s.IdlePerHour += p
		}
	}
	return s
}
