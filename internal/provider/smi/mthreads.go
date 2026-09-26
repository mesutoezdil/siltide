package smi

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// MThreads reads Moore Threads GPUs from the default `mthreads-gmi` table,
// which has no power column.
// ponytail: power needs `-q -d POWER --json`; add once a real sample exists.
func MThreads() Spec {
	return Spec{
		Vendor: device.MThreads, Label: "Moore Threads (mthreads-gmi)",
		Tools: []string{"mthreads-gmi", "/usr/bin/mthreads-gmi"},
		Hint:  "Moore Threads GPUs need the MUSA driver, which installs mthreads-gmi.",
		Parse: func(b []byte) ([]device.Device, error) { return parseMthreadsGmi(string(b)), nil },
	}
}

var (
	mtRow  = regexp.MustCompile(`^(\d+)\s+(.+?)\s*\|\s*([0-9A-Fa-f]{4,8}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}\.[0-7])\s*\|\s*(\d+)%\s+(\d+)MiB\((\d+)MiB\)`)
	mtTemp = regexp.MustCompile(`\|\s*(\d+)C\b`)
)

// parseMthreadsGmi reads
//
//	0    MTT S80        |00000000:01:00.0    |0%    3419MiB(16384MiB)
//	     Physical       |16x(16x)            |59C   YES
func parseMthreadsGmi(text string) []device.Device {
	ls := lines(text)
	var out []device.Device
	for i, l := range ls {
		m := mtRow.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		name := m[2]
		if !strings.HasPrefix(name, "Moore") {
			name = "Moore Threads " + name
		}
		d := device.New(device.MThreads, idx, name, m[3], m[3])
		set(d.Metrics, device.Util, m[4], 1)
		set(d.Metrics, device.MemUsed, m[5], 1<<20)
		set(d.Metrics, device.MemTotal, m[6], 1<<20)
		if i+1 < len(ls) {
			if t := mtTemp.FindStringSubmatch(ls[i+1]); t != nil {
				set(d.Metrics, device.Temp, t[1], 1)
			}
		}
		out = append(out, d)
	}
	return out
}
