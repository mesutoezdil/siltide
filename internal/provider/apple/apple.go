// Package apple reads the Apple silicon GPU without root or cgo: utilization
// and memory from the IOKit registry (`ioreg`), power and energy from
// IOReport, temperature from the SMC (System Management Controller), and
// per-process GPU time from the AGX user clients.
package apple

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

var (
	reModel = regexp.MustCompile(`"model" = "([^"]+)"`)
	reUtil  = regexp.MustCompile(`"Device Utilization %"=(\d+)`)
	reMem   = regexp.MustCompile(`"In use system memory"=(\d+)`)
	reCores = regexp.MustCompile(`"gpu-core-count"\s*=\s*(\d+)`)

	memOnce  sync.Once
	memTotal float64
)

// Provider returns the Apple silicon provider.
func Provider() provider.Provider {
	st := &state{prev: map[int]float64{}}
	return provider.Provider{
		Name: "apple", Label: "Apple silicon (IOKit, IOReport, SMC)",
		Hint: "Apple GPUs are read on an Apple silicon Mac.",
		Detect: func() error {
			if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
				return errors.New("not an Apple silicon Mac")
			}
			_, err := exec.LookPath("ioreg")
			_ = loadDeep() // power and temperature are optional
			return err
		},
		Read: func(ctx context.Context) ([]device.Device, error) {
			out, err := exec.CommandContext(ctx, "ioreg", "-r", "-d", "1", "-c", "IOAccelerator").Output()
			if err != nil {
				return nil, err
			}
			memOnce.Do(func() { // unified memory: the GPU can use all of it
				b, _ := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
				memTotal, _ = strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			})
			devs := Parse(string(out), memTotal)
			if len(devs) == 0 {
				return devs, nil
			}
			d := &devs[0]
			if w, j, ok := power(); ok {
				d.Metrics[device.Power] = w
				st.joules += j
				d.Metrics[device.Energy] = st.joules
			}
			if t, ok := temperature(); ok {
				d.Metrics[device.Temp] = t
			}
			d.Procs = st.procs(gpuTime())
			return devs, nil
		},
	}
}

// state turns accumulated GPU time into a utilization share per process.
type state struct {
	joules float64
	prev   map[int]float64
	at     time.Time
}

func (s *state) procs(times map[int]procTime) []device.Process {
	now := time.Now()
	dt := now.Sub(s.at).Seconds()
	next := map[int]float64{}
	var out []device.Process
	for pid, t := range times {
		next[pid] = t.ns
		p := device.Process{PID: pid, Name: t.name, Metrics: device.Metrics{}}
		if old, ok := s.prev[pid]; ok && dt > 0 && t.ns >= old {
			p.Metrics[device.Util] = min((t.ns-old)/(dt*1e9)*100, 100)
		}
		if t.ns > 0 {
			out = append(out, p)
		}
	}
	s.prev, s.at = next, now
	return out
}

// Parse turns `ioreg -r -d 1 -c IOAccelerator` output into devices.
func Parse(out string, total float64) []device.Device {
	var devs []device.Device
	for _, blk := range strings.Split(out, "+-o ")[1:] {
		name := "Apple GPU"
		if m := reModel.FindStringSubmatch(blk); m != nil {
			name = m[1]
		}
		if m := reCores.FindStringSubmatch(blk); m != nil {
			name += " " + m[1] + "-core GPU"
		}
		d := device.New(device.Apple, len(devs), name, "", "")
		d.Source = "ioreg"
		if m := reUtil.FindStringSubmatch(blk); m != nil {
			d.Metrics[device.Util], _ = strconv.ParseFloat(m[1], 64)
		}
		if m := reMem.FindStringSubmatch(blk); m != nil {
			d.Metrics[device.MemUsed], _ = strconv.ParseFloat(m[1], 64)
		}
		if total > 0 {
			d.Metrics[device.MemTotal] = total
		}
		devs = append(devs, d)
	}
	return devs
}
