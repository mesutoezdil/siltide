package smi

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Cambricon reads MLU cards from the default `cnmon` table.
func Cambricon() Spec {
	return Spec{
		Vendor: device.Cambricon, Label: "Cambricon MLU (cnmon)",
		Tools: []string{"cnmon", "/usr/bin/cnmon", "/usr/local/neuware/bin/cnmon"},
		Hint:  "Cambricon MLUs need the Cambricon driver, which installs cnmon.",
		Parse: func(b []byte) ([]device.Device, error) { return parseCnmon(string(b)), nil },
	}
}

var (
	memPair   = regexp.MustCompile(`(\d+)\s*MiB\s*/\s*(\d+)\s*MiB`)
	powerPair = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*W\s*/\s*(\d+(?:\.\d+)?)\s*W`)
	// |  0     /   12345   python train.py                          1024 MiB          |
	cnProc = regexp.MustCompile(`^\|\s+(\d+)\s+(\S+)\s+(\d+)\s+(.+?)\s+(\d+)\s*MiB\s*\|$`)
)

// parseCnmon reads row pairs:
//
//	| 0     /   MLU370-X4    v1.1.6 | On          v4.20.11 | 0%          N/A       |
//	|  0%   32C         30 W/ 150 W |     0 MiB/ 23308 MiB | 10240 MiB/1048576 MiB |
func parseCnmon(text string) []device.Device {
	ls := lines(text)
	var out []device.Device
	for i := 0; i+1 < len(ls); i++ {
		a, b := cells(ls[i]), cells(ls[i+1])
		if len(a) != 3 || len(b) != 3 || !memPair.MatchString(b[1]) {
			continue
		}
		f := strings.Fields(a[0])
		if len(f) < 3 {
			continue
		}
		idx, ok := deviceIndex(f[0])
		if !ok {
			continue
		}
		d := device.New(device.Cambricon, idx, "Cambricon "+f[2], "", "")
		if m := percent.FindStringSubmatch(a[2]); m != nil {
			set(d.Metrics, device.Util, m[1], 1)
		}
		if m := tempC.FindStringSubmatch(b[0]); m != nil {
			set(d.Metrics, device.Temp, m[1], 1)
		}
		if m := powerPair.FindStringSubmatch(b[0]); m != nil {
			set(d.Metrics, device.Power, m[1], 1)
			set(d.Metrics, device.PowerCap, m[2], 1)
		}
		m := memPair.FindStringSubmatch(b[1])
		set(d.Metrics, device.MemUsed, m[1], 1<<20)
		set(d.Metrics, device.MemTotal, m[2], 1<<20)
		out = append(out, d)
		i++
	}
	for _, l := range ls {
		m := cnProc.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		card, _ := strconv.Atoi(m[1])
		pid, _ := strconv.Atoi(m[3])
		for i := range out {
			if out[i].Index == card {
				pr := device.Process{PID: pid, Name: firstWord(m[4]), Command: m[4], Metrics: device.Metrics{}}
				set(pr.Metrics, device.MemUsed, m[5], 1<<20)
				out[i].Procs = append(out[i].Procs, pr)
			}
		}
	}
	return out
}
