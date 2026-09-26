package smi

import (
	"encoding/csv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Biren reads GPUs from brsmi's CSV query mode.
// ponytail: flags come from Biren's docs but no captured output is public;
// verify on hardware and add power.draw if --help-query-gpu lists it.
func Biren() Spec {
	return Spec{
		Vendor: device.Biren, Label: "Biren (brsmi)",
		Tools: []string{"brsmi", "/usr/bin/brsmi", "/usr/local/bin/brsmi"},
		Hint:  "Biren GPUs need the Biren driver, which installs brsmi.",
		Args:  []string{"gpu", "--query-gpu=index,name,uuid,pci.bus_id,utilization.gpu,memory.used,memory.total,temperature.gpu", "--format=csv,noheader,nounits"},
		Parse: func(b []byte) ([]device.Device, error) { return parseQueryCSV(device.Biren, string(b)), nil },
	}
}

// parseQueryCSV reads index,name,uuid,bus,util,mem used,mem total,temp rows
// with memory in MiB.
func parseQueryCSV(v device.Vendor, text string) []device.Device {
	rd := csv.NewReader(strings.NewReader(text))
	rd.TrimLeadingSpace = true
	rd.FieldsPerRecord = -1
	rows, _ := rd.ReadAll()
	var out []device.Device
	for _, f := range rows {
		if len(f) < 8 {
			continue
		}
		idx, ok := deviceIndex(f[0])
		if !ok {
			continue
		}
		d := device.New(v, idx, f[1], f[2], f[3])
		set(d.Metrics, device.Util, f[4], 1)
		set(d.Metrics, device.MemUsed, f[5], 1<<20)
		set(d.Metrics, device.MemTotal, f[6], 1<<20)
		set(d.Metrics, device.Temp, f[7], 1)
		out = append(out, d)
	}
	return out
}
