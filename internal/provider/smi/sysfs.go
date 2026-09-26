package smi

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

// AMD (amdgpu) and Intel (i915/xe) through the kernel's DRM (Direct
// Rendering Manager) sysfs: no library, no root. Intel exposes no
// utilization or VRAM there.
// ponytail: amdsmi / Level Zero would add per-process data; add when asked.

var (
	drmRoot = "/sys/class/drm"
	pciRoot = "/sys/bus/pci/devices"
	pciIDs  = []string{"/usr/share/hwdata/pci.ids", "/usr/share/misc/pci.ids", "/usr/share/pci.ids"}
)

var cardDir = regexp.MustCompile(`^card\d+$`)

// AMD returns the amdgpu sysfs spec.
func AMD() Spec { return drmSpec(device.AMD, "0x1002", "AMD", "AMD (amdgpu sysfs)") }

// Intel returns the i915/xe sysfs spec.
func Intel() Spec { return drmSpec(device.Intel, "0x8086", "Intel", "Intel (i915/xe sysfs)") }

func drmSpec(v device.Vendor, pciVendor, brand, label string) Spec {
	return Spec{
		Vendor: v, Label: label,
		Hint: brand + " devices appear under " + drmRoot + " once the kernel driver is loaded.",
		Probe: func() error {
			if len(drmCards(drmRoot, pciVendor)) == 0 {
				return fmt.Errorf("no %s card under %s", brand, drmRoot)
			}
			return nil
		},
		Read: func(context.Context, string) ([]device.Device, error) {
			return readDRM(v, drmRoot, pciVendor, brand), nil
		},
	}
}

func drmCards(root, pciVendor string) []string {
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		// cardN only: skip connectors like card0-DP-1.
		if cardDir.MatchString(e.Name()) && readStr(filepath.Join(root, e.Name(), "device", "vendor")) == pciVendor {
			out = append(out, e.Name())
		}
	}
	return out
}

func readDRM(v device.Vendor, root, pciVendor, brand string) []device.Device {
	var out []device.Device
	procs := drmProcs("/proc")
	for _, card := range drmCards(root, pciVendor) {
		dev := filepath.Join(root, card, "device")
		idx, _ := strconv.Atoi(strings.TrimPrefix(card, "card"))
		bus := filepath.Base(resolve(dev))
		d := device.New(v, idx, pciName(pciVendor, readStr(filepath.Join(dev, "device")), brand), bus, bus)
		m := d.Metrics
		set(m, device.Util, readStr(filepath.Join(dev, "gpu_busy_percent")), 1)
		set(m, device.MemUsed, readStr(filepath.Join(dev, "mem_info_vram_used")), 1)
		set(m, device.MemTotal, readStr(filepath.Join(dev, "mem_info_vram_total")), 1)
		if hw, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*")); len(hw) > 0 {
			set(m, device.Temp, readStr(filepath.Join(hw[0], "temp1_input")), 1e-3)     // millidegrees
			set(m, device.Power, readStr(filepath.Join(hw[0], "power1_average")), 1e-6) // microwatts
			if _, ok := m[device.Power]; !ok {
				set(m, device.Power, readStr(filepath.Join(hw[0], "power1_input")), 1e-6)
			}
			set(m, device.PowerCap, readStr(filepath.Join(hw[0], "power1_cap")), 1e-6)
		}
		d.Procs = procs[bus]
		out = append(out, d)
	}
	return out
}

// drmEngine remembers engine time per (pid, device) so the next sample can
// turn it into a utilization percentage.
var drmEngine = struct {
	sync.Mutex
	at   time.Time
	prev map[string]float64
}{prev: map[string]float64{}}

// drmProcs walks /proc/*/fdinfo for DRM clients: the kernel reports the
// device (drm-pdev), VRAM in use (drm-memory-vram, KiB) and busy time per
// engine (drm-engine-*, ns) for every open render node.
func drmProcs(proc string) map[string][]device.Process {
	type key struct {
		pid int
		bus string
	}
	mem := map[key]float64{}
	engine := map[key]float64{}
	names := map[int]string{}
	pids, _ := os.ReadDir(proc)
	for _, e := range pids {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(proc, e.Name(), "fdinfo"))
		if err != nil {
			continue
		}
		seenClient := map[string]bool{}
		for _, fd := range fds {
			b, err := os.ReadFile(filepath.Join(proc, e.Name(), "fdinfo", fd.Name()))
			if err != nil || !strings.Contains(string(b), "drm-pdev:") {
				continue
			}
			var bus, client string
			var vram, busy float64
			for _, l := range strings.Split(string(b), "\n") {
				k, v, ok := strings.Cut(l, ":")
				if !ok {
					continue
				}
				v = strings.TrimSpace(v)
				switch {
				case k == "drm-pdev":
					bus = v
				case k == "drm-client-id":
					client = v
				case k == "drm-memory-vram" || k == "drm-total-vram":
					if n, ok := num(v); ok {
						vram = n * 1024
					}
				case strings.HasPrefix(k, "drm-engine-"):
					if n, ok := num(v); ok {
						busy += n
					}
				}
			}
			if bus == "" || seenClient[client] { // one fd per client is enough
				continue
			}
			seenClient[client] = true
			k := key{pid, bus}
			mem[k] += vram
			engine[k] += busy
		}
		if len(seenClient) > 0 {
			names[pid] = readStr(filepath.Join(proc, e.Name(), "comm"))
		}
	}
	now := time.Now()
	drmEngine.Lock()
	dt := now.Sub(drmEngine.at).Seconds()
	prev := drmEngine.prev
	next := map[string]float64{}
	out := map[string][]device.Process{}
	for k, m := range mem {
		p := device.Process{PID: k.pid, Name: names[k.pid], Metrics: device.Metrics{device.MemUsed: m}}
		id := strconv.Itoa(k.pid) + "/" + k.bus
		next[id] = engine[k]
		if old, ok := prev[id]; ok && dt > 0 && engine[k] >= old {
			p.Metrics[device.Util] = min((engine[k]-old)/(dt*1e9)*100, 100)
		}
		out[k.bus] = append(out[k.bus], p)
	}
	drmEngine.prev, drmEngine.at = next, now
	drmEngine.Unlock()
	return out
}

// VastAI lists Vastai accelerators from PCI sysfs. `vasmi` has no documented
// machine-readable output, so metrics stay N/A.
// ponytail: devices only; parse vasmi once its output format is known.
func VastAI() Spec { return pciSpec(device.VastAI, "0x1ec6", "VastAI", "VastAI (PCI sysfs)") }

func pciSpec(v device.Vendor, pciVendor, brand, label string) Spec {
	return Spec{
		Vendor: v, Label: label,
		Hint: brand + " devices are detected from " + pciRoot + ".",
		Probe: func() error {
			if len(pciDevices(pciRoot, pciVendor)) == 0 {
				return fmt.Errorf("no %s device under %s", brand, pciRoot)
			}
			return nil
		},
		Read: func(context.Context, string) ([]device.Device, error) {
			var out []device.Device
			for i, bus := range pciDevices(pciRoot, pciVendor) {
				id := readStr(filepath.Join(pciRoot, bus, "device"))
				out = append(out, device.New(v, i, pciName(pciVendor, id, brand), bus, bus))
			}
			return out, nil
		},
	}
}

// pciDevices returns bus IDs of accelerator-class functions from vendor.
func pciDevices(root, pciVendor string) []string {
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		// Base class 0x03 display or 0x12 processing accelerator; skips
		// bridges and audio functions on the same card.
		class := readStr(filepath.Join(dir, "class"))
		if readStr(filepath.Join(dir, "vendor")) == pciVendor &&
			(strings.HasPrefix(class, "0x03") || strings.HasPrefix(class, "0x12")) {
			out = append(out, e.Name())
		}
	}
	return out
}

// pciPresent reports whether any accelerator from vendor is on the bus.
// Always true where the bus cannot be inspected.
func pciPresent(pciVendor string) bool {
	if _, err := os.Stat(pciRoot); err != nil {
		return true
	}
	return len(pciDevices(pciRoot, pciVendor)) > 0
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func readStr(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

var (
	pciNamesOnce sync.Once
	pciNames     map[string]string
)

// pciName resolves a device name from the system pci.ids database, falling
// back to "<brand> <device id>".
func pciName(vendor, dev, brand string) string {
	pciNamesOnce.Do(func() { pciNames = loadPCINames(pciIDs) })
	key := strings.TrimPrefix(vendor, "0x") + ":" + strings.TrimPrefix(dev, "0x")
	if n, ok := pciNames[key]; ok {
		return brand + " " + n
	}
	return brand + " " + dev
}

// loadPCINames reads device names of the sysfs vendors from the first
// pci.ids file found.
func loadPCINames(files []string) map[string]string {
	want := map[string]bool{"1002": true, "8086": true, "1ec6": true}
	out := map[string]string{}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		defer func() { _ = fh.Close() }()
		sc := bufio.NewScanner(fh)
		vendor := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "" || line[0] == '#':
			case line[0] != '\t':
				vendor = ""
				if len(line) > 4 && want[line[:4]] {
					vendor = line[:4]
				}
			case vendor != "" && len(line) > 7 && line[1] != '\t':
				out[vendor+":"+line[1:5]] = strings.TrimSpace(line[5:])
			}
		}
		if sc.Err() == nil {
			return out
		}
	}
	return out
}
