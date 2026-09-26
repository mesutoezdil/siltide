package smi

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Ascend reads Huawei Ascend NPUs from the `npu-smi info` table, the only
// output that has every chip in one call (`npu-smi` has no JSON or CSV mode).
func Ascend() Spec {
	return Spec{
		Vendor: device.Ascend, Label: "Huawei Ascend (npu-smi)",
		Tools: []string{"npu-smi", "/usr/local/sbin/npu-smi", "/usr/local/bin/npu-smi"},
		Hint:  "Ascend NPUs need the Ascend driver, which installs npu-smi.",
		Args:  []string{"info"},
		Parse: func(b []byte) ([]device.Device, error) { return parseNpuSmi(string(b)), nil },
	}
}

var slashPair = regexp.MustCompile(`(\d+)\s*/\s*(\d+)`)

// parseNpuSmi walks the device table: an NPU row (id, name, health, power,
// temp) followed by one row per chip (chip, bus id, AICore %, memory). On
// 910 cards the accelerator memory is the HBM (high bandwidth memory) pair;
// 310 cards only have Memory-Usage. Rows with another cell count (process
// table) are skipped.
func parseNpuSmi(text string) []device.Device {
	var out []device.Device
	var name, power, temp string
	npuOf := map[string]int{} // "npu chip" -> device index
	npu := ""
	for _, l := range lines(text) {
		c := cells(l)
		if len(c) == 4 { // process table: NPU chip | pid | name | memory MB
			f := strings.Fields(c[0])
			pid, err := strconv.Atoi(c[1])
			if len(f) != 2 || err != nil {
				continue
			}
			if i, ok := npuOf[f[0]+" "+f[1]]; ok && i < len(out) {
				pr := device.Process{PID: pid, Name: c[2], Metrics: device.Metrics{}}
				set(pr.Metrics, device.MemUsed, c[3], 1<<20)
				out[i].Procs = append(out[i].Procs, pr)
			}
			continue
		}
		if len(c) != 3 {
			continue
		}
		f0, f2 := strings.Fields(c[0]), strings.Fields(c[2])
		if len(f0) == 0 || len(f2) == 0 {
			continue
		}
		if _, err := strconv.Atoi(f0[0]); err != nil {
			continue
		}
		if !busID.MatchString(c[1]) {
			if len(f0) < 2 || len(f2) < 2 {
				continue
			}
			name = "Ascend " + strings.Join(f0[1:], " ")
			power, temp = f2[0], f2[1]
			npu = f0[0]
			continue
		}
		npuOf[npu+" "+f0[0]] = len(out)
		d := device.New(device.Ascend, len(out), name, c[1], c[1])
		set(d.Metrics, device.Util, f2[0], 1)
		set(d.Metrics, device.Temp, temp, 1)
		set(d.Metrics, device.Power, power, 1)
		// Last pair with a non-zero total: HBM on 910, Memory-Usage on 310.
		for _, m := range slashPair.FindAllStringSubmatch(c[2], -1) {
			if total, ok := num(m[2]); ok && total > 0 {
				set(d.Metrics, device.MemUsed, m[1], 1<<20)
				set(d.Metrics, device.MemTotal, m[2], 1<<20)
			}
		}
		out = append(out, d)
	}
	return out
}
