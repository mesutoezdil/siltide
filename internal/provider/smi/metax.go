package smi

import (
	"regexp"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// MetaX reads GPUs from the default `mx-smi` table. Its column order changed
// between releases, so values are matched by unit, not position.
// ponytail: mx-smi -j exists but no sample of it is public; switch once seen.
func MetaX() Spec {
	return Spec{
		Vendor: device.MetaX, Label: "MetaX (mx-smi)",
		Tools: []string{"mx-smi", "/opt/mxdriver/bin/mx-smi", "/opt/maca/bin/mx-smi"},
		Hint:  "MetaX GPUs need the MetaX driver, which installs mx-smi.",
		Parse: func(b []byte) ([]device.Device, error) { return parseMxSmi(string(b)), nil },
	}
}

var (
	mxMem   = regexp.MustCompile(`(\d+)\s*/\s*(\d+)\s*MiB`)
	mxPower = regexp.MustCompile(`(\d+(?:\.\d+)?)W\s*/\s*(\d+(?:\.\d+)?)W`)
)

// parseMxSmi reads row pairs; the first has the bus id, the second the
// memory pair:
//
//	| 0     MetaX C500 | 0           Off | 0000:0e:00.0        | 0%          Disabled |
//	| 57W / 350W       | 35C          P0 | 826/65536 MiB       | Available            |
func parseMxSmi(text string) []device.Device {
	ls := lines(text)
	var out []device.Device
	for i := 0; i+1 < len(ls); i++ {
		a := cells(ls[i])
		if len(a) < 3 || !mxMem.MatchString(ls[i+1]) {
			continue
		}
		bus := ""
		for _, c := range a {
			if busID.MatchString(c) {
				bus = c
			}
		}
		f := strings.Fields(a[0])
		if bus == "" || len(f) == 0 {
			continue
		}
		idx, ok := deviceIndex(f[0])
		if !ok {
			continue
		}
		var name []string
		for _, w := range f[1:] {
			if w == "On" || w == "Off" {
				break
			}
			name = append(name, w)
		}
		d := device.New(device.MetaX, idx, strings.Join(name, " "), bus, bus)
		if m := percent.FindStringSubmatch(a[len(a)-1]); m != nil {
			set(d.Metrics, device.Util, m[1], 1)
		}
		row2 := ls[i+1]
		if m := tempC.FindStringSubmatch(row2); m != nil {
			set(d.Metrics, device.Temp, m[1], 1)
		}
		if m := mxPower.FindStringSubmatch(row2); m != nil {
			set(d.Metrics, device.Power, m[1], 1)
			set(d.Metrics, device.PowerCap, m[2], 1)
		}
		m := mxMem.FindStringSubmatch(row2)
		set(d.Metrics, device.MemUsed, m[1], 1<<20)
		set(d.Metrics, device.MemTotal, m[2], 1<<20)
		out = append(out, d)
		i++
	}
	return out
}
