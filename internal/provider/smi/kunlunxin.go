package smi

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Kunlunxin reads XPUs from `xpu_smi -m`, one positional line per device.
// Intel also ships a tool named `xpu-smi`, so that name is only trusted when a
// Kunlunxin PCI device (vendor 0x1d22) is present.
func Kunlunxin() Spec {
	return Spec{
		Vendor: device.Kunlunxin, Label: "Kunlunxin XPU (xpu_smi)",
		Tools: []string{"xpu_smi", "xpu-smi", "/usr/local/bin/xpu-smi", "/usr/local/xpu/bin/xpu_smi"},
		Hint:  "Kunlunxin XPUs need the XRE driver, which installs xpu_smi.",
		Args:  []string{"-m"},
		Read: func(ctx context.Context, path string) ([]device.Device, error) {
			if filepath.Base(path) == "xpu-smi" && !pciPresent("0x1d22") {
				return nil, errors.New("xpu-smi is not Kunlunxin's (no 0x1d22 device)")
			}
			out, err := run(ctx, path, "-m")
			if err != nil {
				return nil, err
			}
			return parseXpuSmi(string(out)), nil
		},
		Parse: func(b []byte) ([]device.Device, error) { return parseXpuSmi(string(b)), nil },
	}
}

var xpuToken = regexp.MustCompile(`"[^"]*"|\S+`)

// parseXpuSmi reads lines like
//
//	00000000:03:00.0 0 0 02K15K624CV00304 36 0 0 0 86 ... 90440 98304 0 1.0:2.6:1.39.1.7 "P800 OAM" ...
//
// Columns: 0 bus, 2 device, 3 serial, 4 temp C, 8 power W, 17/18 memory
// used/total MiB, 19 util %, 21 quoted model.
func parseXpuSmi(text string) []device.Device {
	var out []device.Device
	for _, l := range lines(text) {
		t := xpuToken.FindAllString(l, -1)
		if len(t) < 22 || !busID.MatchString(t[0]) {
			continue
		}
		idx, ok := deviceIndex(t[2])
		if !ok {
			continue
		}
		d := device.New(device.Kunlunxin, idx, "Kunlunxin "+strings.Trim(t[21], `"`), t[3], t[0])
		set(d.Metrics, device.Temp, t[4], 1)
		set(d.Metrics, device.Power, t[8], 1)
		set(d.Metrics, device.MemUsed, t[17], 1<<20)
		set(d.Metrics, device.MemTotal, t[18], 1<<20)
		set(d.Metrics, device.Util, t[19], 1)
		out = append(out, d)
	}
	return out
}
