package smi

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/moezdil/siltide/internal/device"
)

// Enflame reads GCUs from `efsmi -q` key:value blocks.
func Enflame() Spec {
	return Spec{
		Vendor: device.Enflame, Label: "Enflame GCU (efsmi)",
		Tools: []string{"efsmi", "/usr/local/efsmi/efsmi", "/usr/bin/efsmi"},
		Hint:  "Enflame GCUs need the Enflame driver, which installs efsmi.",
		Args:  []string{"-q", "-d", "DEVICE,POWER,TEMP,MEMORY,USAGE,PCIE"},
		Parse: func(b []byte) ([]device.Device, error) { return parseEfsmi(string(b)), nil },
	}
}

var efsmiDev = regexp.MustCompile(`^DEV ID (\d+)$`)

func parseEfsmi(text string) []device.Device {
	var out []device.Device
	for _, b := range kvBlocks(text, efsmiDev) {
		idx, _ := strconv.Atoi(b["#"])
		name := b.first("Dev Name")
		if name == "" {
			name = "Enflame GCU"
		}
		bus := ""
		if b["Bus"] != "" {
			bus = fmt.Sprintf("%s:%s:%s.%s", b["Domain"], b["Bus"], b["Dev"], b["Func"])
		}
		d := device.New(device.Enflame, idx, name, b.first("Dev UUID", "Dev SN"), bus)
		set(d.Metrics, device.Util, b.first("GCU Usage"), 1)
		setBytes(d.Metrics, device.MemUsed, b.first("Used Size", "Mem Usage"), "mib")
		setBytes(d.Metrics, device.MemTotal, b.first("Total Size", "Mem Size"), "mib")
		set(d.Metrics, device.Temp, b.first("GCU Temp"), 1)
		set(d.Metrics, device.Power, b.first("Cur Power"), 1)
		set(d.Metrics, device.PowerCap, b.first("Power Capa"), 1)
		out = append(out, d)
	}
	return out
}
