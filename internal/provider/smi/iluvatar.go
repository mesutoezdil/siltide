package smi

import (
	"context"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/moezdil/siltide/internal/device"
)

// Iluvatar reads CoreX GPUs from `ixsmi -q -x`, an XML device log.
func Iluvatar() Spec {
	return Spec{
		Vendor: device.Iluvatar, Label: "Iluvatar CoreX (ixsmi)",
		Tools: []string{"ixsmi", "/usr/local/corex/bin/ixsmi", "/usr/local/corex*/bin/ixsmi"},
		Hint:  "Iluvatar GPUs need the CoreX SDK, which installs ixsmi under /usr/local/corex.",
		Args:  []string{"-q", "-x"},
		Env:   []string{"LD_LIBRARY_PATH=/usr/local/corex/lib:/usr/local/corex/lib64"},
		Parse: parseIxsmi,
		Read: func(ctx context.Context, path string) ([]device.Device, error) {
			// `ixsmi` needs the CoreX libraries next to it.
			root := filepath.Dir(filepath.Dir(path))
			env := []string{"LD_LIBRARY_PATH=" + root + "/lib:" + root + "/lib64"}
			out, err := runEnv(ctx, env, path, "-q", "-x")
			if err != nil {
				return nil, err
			}
			return parseIxsmi(out)
		},
	}
}

type ixLog struct {
	GPUs []struct {
		Bus    string `xml:"id,attr"`
		Name   string `xml:"product_name"`
		UUID   string `xml:"uuid"`
		Minor  string `xml:"minor_number"`
		Total  string `xml:"memory_usage>total"`
		Used   string `xml:"memory_usage>used"`
		FBTot  string `xml:"fb_memory_usage>total"`
		FBUsed string `xml:"fb_memory_usage>used"`
		Util   string `xml:"utilization>gpu_util"`
		Temp   string `xml:"temperature>gpu_temp"`
		Power  string `xml:"power_readings>gpu_power_draw"`
		Board  string `xml:"power_readings>board_power_draw"`
		Limit  string `xml:"power_readings>current_gpu_power_limit"`
	} `xml:"gpu"`
}

func parseIxsmi(data []byte) ([]device.Device, error) {
	var log ixLog
	if err := xml.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("ixsmi xml: %w", err)
	}
	out := make([]device.Device, 0, len(log.GPUs))
	for i, g := range log.GPUs {
		idx, err := strconv.Atoi(g.Minor)
		if err != nil {
			idx = i
		}
		d := device.New(device.Iluvatar, idx, g.Name, g.UUID, g.Bus)
		m := d.Metrics
		set(m, device.Util, g.Util, 1)
		setBytes(m, device.MemUsed, g.Used, "mib")
		setBytes(m, device.MemTotal, g.Total, "mib")
		if _, ok := m[device.MemTotal]; !ok {
			setBytes(m, device.MemUsed, g.FBUsed, "mib")
			setBytes(m, device.MemTotal, g.FBTot, "mib")
		}
		set(m, device.Temp, g.Temp, 1)
		set(m, device.Power, g.Power, 1)
		if _, ok := m[device.Power]; !ok {
			set(m, device.Power, g.Board, 1)
		}
		set(m, device.PowerCap, g.Limit, 1)
		out = append(out, d)
	}
	return out, nil
}
