package nvidia

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

// NVML (the NVIDIA Management Library) is loaded at runtime with purego, so
// the binary is static and still starts on hosts without the NVIDIA driver.
// Only the calls siltide needs are bound; optional ones degrade to absent
// metrics when the driver is older.

var lib struct {
	once sync.Once
	err  error

	init     func() int32
	shutdown func() int32
	count    func(*uint32) int32
	handle   func(uint32, *uintptr) int32
	name     func(uintptr, *byte, uint32) int32
	uuid     func(uintptr, *byte, uint32) int32
	util     func(uintptr, *utilization) int32
	meminfo  func(uintptr, *memory) int32
	// optional; preferred over meminfo when the driver has it (see types.go)
	meminfoV2 func(uintptr, *memoryV2) int32
	temp      func(uintptr, uint32, *uint32) int32
	power     func(uintptr, *uint32) int32
	// optional
	pci      func(uintptr, *pciInfo) int32
	powerCap func(uintptr, *uint32) int32
	clock    func(uintptr, uint32, *uint32) int32
	fan      func(uintptr, *uint32) int32
	throttle func(uintptr, *uint64) int32
	ecc      func(uintptr, uint32, uint32, *uint64) int32
	compute  func(uintptr, *uint32, *processInfo) int32
	graphics func(uintptr, *uint32, *processInfo) int32
	pcieGen  func(uintptr, *uint32) int32
	pcieW    func(uintptr, *uint32) int32
	pcieMaxG func(uintptr, *uint32) int32
	pcieMaxW func(uintptr, *uint32) int32
	pcieTput func(uintptr, uint32, *uint32) int32 // KB/s, counter 0 tx 1 rx
	nvlink   func(uintptr, uint32, *uint32) int32 // link, *enabled
	encoder  func(uintptr, *uint32, *uint32) int32
	decoder  func(uintptr, *uint32, *uint32) int32
	migMode  func(uintptr, *uint32, *uint32) int32
	migMax   func(uintptr, *uint32) int32
	migByIdx func(uintptr, uint32, *uintptr) int32
	// depth
	remapped   func(uintptr, *uint32, *uint32, *uint32, *uint32) int32 // corr, unc, pending, failed
	retired    func(uintptr, uint32, *uint32, *uint64) int32           // cause, *count, addrs
	replays    func(uintptr, *uint32) int32
	violations func(uintptr, uint32, *violation) int32 // policy 0 power, 1 thermal
	energy     func(uintptr, *uint64) int32            // mJ
	pstate     func(uintptr, *uint32) int32
	threshold  func(uintptr, uint32, *uint32) int32 // 0 shutdown, 1 slowdown
	fields     func(uintptr, int32, *fieldValue) int32
	linkVer    func(uintptr, uint32, *uint32) int32
	linkPeer   func(uintptr, uint32, *pciInfo) int32
	linkErr    func(uintptr, uint32, uint32, *uint64) int32 // link, counter 0..3
	procUtil   func(uintptr, *processUtil, *uint32, uint64) int32
	evCreate   func(*uintptr) int32
	evRegister func(uintptr, uint64, uintptr) int32
	evWait     func(uintptr, *eventData, uint32) int32
	evFree     func(uintptr) int32
	numa       func(uintptr, *uint32) int32
	topo       func(uintptr, uintptr, *uint32) int32
}

func bind(h uintptr, fptr any, names ...string) bool {
	for _, n := range names {
		if sym, err := purego.Dlsym(h, n); err == nil {
			purego.RegisterFunc(fptr, sym)
			return true
		}
	}
	return false
}

const (
	rcOK           = 0
	rcInsufficient = 7 // the buffer was too small; count holds the need
	tempGPU        = 0
	clockGraphics  = 0
	clockMem       = 2
	eccCorrected   = 0
	eccUncorrected = 1
	eccAggregate   = 1
	memNotKnown    = ^uint64(0)

	eventXid       = 0x8
	eventSingleBit = 0x1
	eventDoubleBit = 0x2
	eventMig       = 0x100
	eventAll       = eventXid | eventSingleBit | eventDoubleBit | eventMig

	fieldNvlinkTx = 90 // NVML_FI_DEV_NVLINK_THROUGHPUT_DATA_TX, KiB
	fieldNvlinkRx = 91
	fieldMemTemp  = 82 // NVML_FI_DEV_MEMORY_TEMP

	nvmlMemoryV2Version = 40 | (2 << 24) // sizeof(nvmlMemory_v2_t) is 40 bytes
)

// libName is the library to load; tests point it at a fake.
var libName = "libnvidia-ml.so.1"

func load() error {
	lib.once.Do(func() {
		h, err := purego.Dlopen(libName, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = errors.New("libnvidia-ml.so.1 not found (NVIDIA driver not installed)")
			return
		}
		required := []struct {
			fn    any
			names []string
		}{
			{&lib.init, []string{"nvmlInit_v2", "nvmlInit"}},
			{&lib.shutdown, []string{"nvmlShutdown"}},
			{&lib.count, []string{"nvmlDeviceGetCount_v2", "nvmlDeviceGetCount"}},
			{&lib.handle, []string{"nvmlDeviceGetHandleByIndex_v2", "nvmlDeviceGetHandleByIndex"}},
			{&lib.name, []string{"nvmlDeviceGetName"}},
			{&lib.uuid, []string{"nvmlDeviceGetUUID"}},
			{&lib.util, []string{"nvmlDeviceGetUtilizationRates"}},
			{&lib.meminfo, []string{"nvmlDeviceGetMemoryInfo"}},
			{&lib.temp, []string{"nvmlDeviceGetTemperature"}},
			{&lib.power, []string{"nvmlDeviceGetPowerUsage"}},
		}
		for _, r := range required {
			if !bind(h, r.fn, r.names...) {
				lib.err = fmt.Errorf("nvml: %s missing", r.names[0])
				return
			}
		}
		bind(h, &lib.meminfoV2, "nvmlDeviceGetMemoryInfo_v2")
		bind(h, &lib.pci, "nvmlDeviceGetPciInfo_v3")
		bind(h, &lib.powerCap, "nvmlDeviceGetEnforcedPowerLimit")
		bind(h, &lib.clock, "nvmlDeviceGetClockInfo")
		bind(h, &lib.fan, "nvmlDeviceGetFanSpeed")
		bind(h, &lib.throttle, "nvmlDeviceGetCurrentClocksEventReasons", "nvmlDeviceGetCurrentClocksThrottleReasons")
		bind(h, &lib.ecc, "nvmlDeviceGetTotalEccErrors")
		bind(h, &lib.compute, "nvmlDeviceGetComputeRunningProcesses_v3", "nvmlDeviceGetComputeRunningProcesses_v2")
		bind(h, &lib.graphics, "nvmlDeviceGetGraphicsRunningProcesses_v3", "nvmlDeviceGetGraphicsRunningProcesses_v2")
		bind(h, &lib.pcieGen, "nvmlDeviceGetCurrPcieLinkGeneration")
		bind(h, &lib.pcieW, "nvmlDeviceGetCurrPcieLinkWidth")
		bind(h, &lib.pcieMaxG, "nvmlDeviceGetMaxPcieLinkGeneration")
		bind(h, &lib.pcieMaxW, "nvmlDeviceGetMaxPcieLinkWidth")
		bind(h, &lib.pcieTput, "nvmlDeviceGetPcieThroughput")
		bind(h, &lib.nvlink, "nvmlDeviceGetNvLinkState")
		bind(h, &lib.encoder, "nvmlDeviceGetEncoderUtilization")
		bind(h, &lib.decoder, "nvmlDeviceGetDecoderUtilization")
		bind(h, &lib.migMode, "nvmlDeviceGetMigMode")
		bind(h, &lib.migMax, "nvmlDeviceGetMaxMigDeviceCount")
		bind(h, &lib.migByIdx, "nvmlDeviceGetMigDeviceHandleByIndex")
		bind(h, &lib.remapped, "nvmlDeviceGetRemappedRows")
		bind(h, &lib.retired, "nvmlDeviceGetRetiredPages")
		bind(h, &lib.replays, "nvmlDeviceGetPcieReplayCounter")
		bind(h, &lib.violations, "nvmlDeviceGetViolationStatus")
		bind(h, &lib.energy, "nvmlDeviceGetTotalEnergyConsumption")
		bind(h, &lib.pstate, "nvmlDeviceGetPerformanceState")
		bind(h, &lib.threshold, "nvmlDeviceGetTemperatureThreshold")
		bind(h, &lib.fields, "nvmlDeviceGetFieldValues")
		bind(h, &lib.linkVer, "nvmlDeviceGetNvLinkVersion")
		bind(h, &lib.linkPeer, "nvmlDeviceGetNvLinkRemotePciInfo_v2")
		bind(h, &lib.linkErr, "nvmlDeviceGetNvLinkErrorCounter")
		bind(h, &lib.procUtil, "nvmlDeviceGetProcessUtilization")
		bind(h, &lib.evCreate, "nvmlEventSetCreate")
		bind(h, &lib.evRegister, "nvmlDeviceRegisterEvents")
		bind(h, &lib.evWait, "nvmlEventSetWait_v2", "nvmlEventSetWait")
		bind(h, &lib.evFree, "nvmlEventSetFree")
		bind(h, &lib.numa, "nvmlDeviceGetNumaNodeId")
		bind(h, &lib.topo, "nvmlDeviceGetTopologyCommonAncestor")
		if rc := lib.init(); rc != rcOK {
			lib.err = fmt.Errorf("nvmlInit failed (code %d)", rc)
		}
	})
	return lib.err
}

// Provider returns the NVML provider.
func Provider() provider.Provider {
	return provider.Provider{
		Name: "nvidia", Label: "NVIDIA (NVML)",
		Hint:   "NVIDIA GPUs need the NVIDIA driver, which installs libnvidia-ml.so.1.",
		Detect: load,
		Read:   read,
		Events: events,
		Close: func() {
			if lib.err == nil && lib.shutdown != nil {
				lib.shutdown()
			}
		},
	}
}

// readMemory returns used and total device memory in bytes. It prefers
// nvmlDeviceGetMemoryInfo_v2, whose Used excludes the reserved region a
// modern driver carves out; the plain call folds that region into Used to
// keep its own Total = Used + Free, which overstates what a workload
// actually holds by several hundred megabytes on an otherwise idle H100.
func readMemory(h uintptr) (used, total uint64, ok bool) {
	if lib.meminfoV2 != nil {
		m := memoryV2{Version: nvmlMemoryV2Version}
		if lib.meminfoV2(h, &m) == rcOK {
			return m.Used, m.Total, true
		}
	}
	var m memory
	if lib.meminfo(h, &m) == rcOK {
		return m.Used, m.Total, true
	}
	return 0, 0, false
}

func cstr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func read(context.Context) ([]device.Device, error) {
	var n uint32
	if rc := lib.count(&n); rc != rcOK {
		return nil, fmt.Errorf("nvml: device count failed (code %d)", rc)
	}
	out := make([]device.Device, 0, n)
	handles := make([]uintptr, n)
	for i := uint32(0); i < n; i++ {
		lib.handle(i, &handles[i])
	}
	for i := uint32(0); i < n; i++ {
		h := handles[i]
		if h == 0 {
			continue
		}
		buf := make([]byte, 96)
		name := "NVIDIA GPU"
		if lib.name(h, &buf[0], uint32(len(buf))) == rcOK {
			name = cstr(buf)
		}
		uuid := ""
		if lib.uuid(h, &buf[0], uint32(len(buf))) == rcOK {
			uuid = cstr(buf)
		}
		bus := ""
		if lib.pci != nil {
			var p pciInfo
			if lib.pci(h, &p) == rcOK {
				bus = cstr(p.BusID[:])
			}
		}
		d := device.New(device.NVIDIA, int(i), name, uuid, bus)
		d.Source = "nvml"
		m := d.Metrics
		var u utilization
		if lib.util(h, &u) == rcOK {
			m[device.Util], m[device.MemBandwidth] = float64(u.GPU), float64(u.Memory)
		}
		if used, total, ok := readMemory(h); ok {
			m[device.MemUsed], m[device.MemTotal] = float64(used), float64(total)
		}
		var v32 uint32
		if lib.temp(h, tempGPU, &v32) == rcOK {
			m[device.Temp] = float64(v32)
		}
		if lib.power(h, &v32) == rcOK {
			m[device.Power] = float64(v32) / 1000
		}
		if lib.powerCap != nil && lib.powerCap(h, &v32) == rcOK {
			m[device.PowerCap] = float64(v32) / 1000
		}
		if lib.clock != nil {
			if lib.clock(h, clockGraphics, &v32) == rcOK {
				m[device.ClockCore] = float64(v32)
			}
			if lib.clock(h, clockMem, &v32) == rcOK {
				m[device.ClockMem] = float64(v32)
			}
		}
		if lib.fan != nil && lib.fan(h, &v32) == rcOK {
			m[device.Fan] = float64(v32)
		}
		var v64 uint64
		if lib.throttle != nil && lib.throttle(h, &v64) == rcOK {
			m[device.Throttle] = float64(throttleBits(v64))
		}
		if lib.ecc != nil {
			if lib.ecc(h, eccCorrected, eccAggregate, &v64) == rcOK {
				m[device.EccCorrected] = float64(v64)
			}
			if lib.ecc(h, eccUncorrected, eccAggregate, &v64) == rcOK {
				m[device.EccUncorrected] = float64(v64)
			}
		}
		u32 := func(f func(uintptr, *uint32) int32, k device.Metric) {
			if f != nil && f(h, &v32) == rcOK {
				m[k] = float64(v32)
			}
		}
		u32(lib.pcieGen, device.PCIeGen)
		u32(lib.pcieW, device.PCIeWidth)
		u32(lib.pcieMaxG, device.PCIeMaxGen)
		u32(lib.pcieMaxW, device.PCIeMaxWidth)
		if lib.pcieTput != nil {
			if lib.pcieTput(h, 0, &v32) == rcOK {
				m[device.PCIeTx] = float64(v32) * 1000
			}
			if lib.pcieTput(h, 1, &v32) == rcOK {
				m[device.PCIeRx] = float64(v32) * 1000
			}
		}
		d.Links = links(h)
		if len(d.Links) > 0 {
			active := 0
			for _, l := range d.Links {
				if l.Active {
					active++
				}
			}
			m[device.LinksTotal], m[device.LinksActive] = float64(len(d.Links)), float64(active)
		}
		depth(h, m)
		var sampling uint32
		if lib.encoder != nil && lib.encoder(h, &v32, &sampling) == rcOK {
			m[device.Encoder] = float64(v32)
		}
		if lib.decoder != nil && lib.decoder(h, &v32, &sampling) == rcOK {
			m[device.Decoder] = float64(v32)
		}
		if lib.numa != nil && lib.numa(h, &v32) == rcOK && v32 != 0xFFFFFFFF {
			m[device.NUMANode] = float64(v32)
		}
		aer(bus, m)
		if lib.topo != nil && n > 1 {
			d.Topology = map[int]string{}
			for j := uint32(0); j < n; j++ {
				var level uint32
				if j != i && handles[j] != 0 && lib.topo(h, handles[j], &level) == rcOK {
					d.Topology[int(j)] = topoName(level, d.Links, int(j))
				}
			}
		}
		d.Procs = append(procs(h, lib.compute), procs(h, lib.graphics)...)
		procUtilization(h, d.Procs)
		parts := partitions(h, d)
		if parts != nil {
			m[device.Partitions] = float64(len(parts))
		}
		out = append(out, d)
		out = append(out, parts...)
	}
	return out, nil
}

// topoName maps the NVML topology level enum onto the names `nvidia-smi
// topo` prints; a peer reached over NVLink is shown as NV<links>.
func topoName(level uint32, links []device.Link, peer int) string {
	names := map[uint32]string{0: "SELF", 10: "PIX", 20: "PXB", 30: "PHB", 40: "NODE", 50: "SYS"}
	name, ok := names[level]
	if !ok {
		name = fmt.Sprint(level)
	}
	if level >= 40 && len(links) > 0 { // far apart on PCIe but joined by NVLink
		return fmt.Sprintf("NV%d", len(links))
	}
	_ = peer
	return name
}

// aer reads the PCIe Advanced Error Reporting counters the kernel keeps
// for the device's bus address (the TOTAL_ERR_* line of each file).
func aer(bus string, m device.Metrics) {
	if bus == "" {
		return
	}
	dir := "/sys/bus/pci/devices/" + strings.ToLower(bus)
	total := func(name string) (float64, bool) {
		b, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			return 0, false
		}
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, " "); ok && strings.HasPrefix(k, "TOTAL_") {
				n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				return n, err == nil
			}
		}
		return 0, false
	}
	if v, ok := total("aer_dev_correctable"); ok {
		m[device.AERCorrected] = v
	}
	if v, ok := total("aer_dev_fatal"); ok {
		f, _ := total("aer_dev_nonfatal")
		m[device.AERFatal] = v + f
	}
}

// depth reads the reliability and thermal counters.
func depth(h uintptr, m device.Metrics) {
	var a, b, c, e uint32
	if lib.remapped != nil && lib.remapped(h, &a, &b, &c, &e) == rcOK {
		m[device.RemappedRows] = float64(a + b)
		m[device.RemapPending] = float64(c)
		m[device.RemapFailed] = float64(e)
	}
	if lib.retired != nil {
		total, ok := 0.0, false
		for cause := uint32(0); cause < 2; cause++ { // 0 multiple single-bit, 1 double-bit
			var n uint32
			rc := lib.retired(h, cause, &n, nil)
			if rc == rcOK || rc == rcInsufficient {
				total += float64(n)
				ok = true
			}
		}
		if ok {
			m[device.RetiredPages] = total
		}
	}
	var v32 uint32
	if lib.replays != nil && lib.replays(h, &v32) == rcOK {
		m[device.PCIeReplays] = float64(v32)
	}
	if lib.violations != nil {
		for policy, k := range map[uint32]device.Metric{0: device.ViolationPower, 1: device.ViolationTherm} {
			var v violation
			if lib.violations(h, policy, &v) == rcOK && v.Reference > 0 {
				m[k] = float64(v.Violation) / float64(v.Reference) * 100
			}
		}
	}
	var v64 uint64
	if lib.energy != nil && lib.energy(h, &v64) == rcOK {
		m[device.Energy] = float64(v64) / 1000
	}
	if lib.pstate != nil && lib.pstate(h, &v32) == rcOK && v32 < 32 {
		m[device.PState] = float64(v32)
	}
	if lib.threshold != nil {
		if lib.threshold(h, 0, &v32) == rcOK {
			m[device.TempShutdown] = float64(v32)
		}
		if lib.threshold(h, 1, &v32) == rcOK {
			m[device.TempSlowdown] = float64(v32)
		}
	}
	if lib.fields != nil {
		f := fieldValue{ID: fieldMemTemp}
		if lib.fields(h, 1, &f) == rcOK && f.Return == rcOK {
			m[device.MemTemp] = float64(f.Value)
		}
	}
}

// links reads every NVLink: state, version, peer, throughput, and errors.
func links(h uintptr) []device.Link {
	if lib.nvlink == nil {
		return nil
	}
	var out []device.Link
	for link := uint32(0); link < 18; link++ {
		var on uint32
		if lib.nvlink(h, link, &on) != rcOK {
			break
		}
		l := device.Link{Index: int(link), Active: on == 1}
		var v32 uint32
		if lib.linkVer != nil && lib.linkVer(h, link, &v32) == rcOK {
			l.Version = fmt.Sprint(v32)
		}
		if lib.linkPeer != nil {
			var p pciInfo
			if lib.linkPeer(h, link, &p) == rcOK {
				l.Peer = cstr(p.BusID[:])
			}
		}
		if lib.linkErr != nil {
			for counter := uint32(0); counter < 4; counter++ {
				var v64 uint64
				if lib.linkErr(h, link, counter, &v64) == rcOK {
					l.Errors += v64
				}
			}
		}
		if lib.fields != nil && l.Active {
			fs := [2]fieldValue{{ID: fieldNvlinkTx, Scope: link}, {ID: fieldNvlinkRx, Scope: link}}
			if lib.fields(h, 2, &fs[0]) == rcOK {
				// counters in KiB; the field carries a latency window in µs
				if fs[0].Return == rcOK && fs[0].Latency > 0 {
					l.Tx = float64(fs[0].Value) * 1024 / (float64(fs[0].Latency) / 1e6)
				}
				if fs[1].Return == rcOK && fs[1].Latency > 0 {
					l.Rx = float64(fs[1].Value) * 1024 / (float64(fs[1].Latency) / 1e6)
				}
			}
		}
		out = append(out, l)
	}
	return out
}

// procUtilization adds SM (streaming multiprocessor) and memory utilization
// per process from the driver's sample buffer (needs a recent sample; absent
// otherwise).
func procUtilization(h uintptr, procs []device.Process) {
	if lib.procUtil == nil || len(procs) == 0 {
		return
	}
	n := uint32(64)
	buf := make([]processUtil, n)
	rc := lib.procUtil(h, &buf[0], &n, 0)
	if rc == rcInsufficient {
		buf = make([]processUtil, n+8)
		n = uint32(len(buf))
		rc = lib.procUtil(h, &buf[0], &n, 0)
	}
	if rc != rcOK {
		return
	}
	by := map[int]processUtil{}
	for _, s := range buf[:n] {
		by[int(s.PID)] = s
	}
	for i := range procs {
		if s, ok := by[procs[i].PID]; ok {
			procs[i].Metrics[device.SMUtil] = float64(s.SM)
			procs[i].Metrics[device.Util] = float64(s.SM)
			procs[i].Metrics[device.MemBandwidth] = float64(s.Mem)
		}
	}
}

// events blocks on the NVML event set and reports Xids, ECC (error-correcting
// code) errors, and MIG (Multi-Instance GPU) changes until ctx ends.
func events(ctx context.Context, emit func(provider.Event)) {
	if lib.err != nil || lib.evCreate == nil || lib.evRegister == nil || lib.evWait == nil {
		return
	}
	var set uintptr
	if lib.evCreate(&set) != rcOK {
		return
	}
	defer func() {
		if lib.evFree != nil {
			lib.evFree(set)
		}
	}()
	var n uint32
	if lib.count(&n) != rcOK {
		return
	}
	ids := map[uintptr]string{}
	for i := uint32(0); i < n; i++ {
		var h uintptr
		if lib.handle(i, &h) != rcOK {
			continue
		}
		buf := make([]byte, 96)
		id := fmt.Sprintf("nvidia-%d", i)
		if lib.uuid(h, &buf[0], uint32(len(buf))) == rcOK {
			id = "nvidia-" + cstr(buf)
		}
		ids[h] = id
		lib.evRegister(h, eventAll, set) // devices without support are skipped
	}
	if len(ids) == 0 {
		return
	}
	for ctx.Err() == nil {
		var ev eventData
		rc := lib.evWait(set, &ev, 1000)
		if rc != rcOK { // timeout or error: poll ctx again
			continue
		}
		e := provider.Event{DeviceID: ids[ev.Device], Code: int(ev.Data)}
		switch {
		case ev.Type&eventXid != 0:
			e.Kind = "xid"
			e.Severity, e.Message = describe(int(ev.Data))
		case ev.Type&eventDoubleBit != 0:
			e.Kind, e.Severity, e.Message = "ecc", "critical", "double-bit ECC error"
		case ev.Type&eventSingleBit != 0:
			e.Kind, e.Severity, e.Message = "ecc", "info", "single-bit ECC error"
		case ev.Type&eventMig != 0:
			e.Kind, e.Severity, e.Message = "mig", "info", "MIG configuration changed"
		default:
			continue
		}
		emit(e)
	}
}

// partitions lists the MIG instances of a device in MIG mode. Each becomes
// a device of its own with Parent set; NVML reports their memory but not
// their utilization.
func partitions(h uintptr, parent device.Device) []device.Device {
	if lib.migMode == nil || lib.migMax == nil || lib.migByIdx == nil {
		return nil
	}
	var cur, pending, n uint32
	if lib.migMode(h, &cur, &pending) != rcOK || cur != 1 || lib.migMax(h, &n) != rcOK {
		return nil
	}
	var out []device.Device
	for i := uint32(0); i < n; i++ {
		var mh uintptr
		if lib.migByIdx(h, i, &mh) != rcOK {
			continue
		}
		buf := make([]byte, 96)
		name, uuid := parent.Name+" partition", ""
		if lib.name(mh, &buf[0], uint32(len(buf))) == rcOK {
			name = cstr(buf)
		}
		if lib.uuid(mh, &buf[0], uint32(len(buf))) == rcOK {
			uuid = cstr(buf)
		}
		p := device.New(device.NVIDIA, int(i), name, uuid, parent.Bus)
		p.Source, p.Parent = "nvml", parent.ID
		if used, total, ok := readMemory(mh); ok {
			p.Metrics[device.MemUsed], p.Metrics[device.MemTotal] = float64(used), float64(total)
		}
		var u utilization
		if lib.util(mh, &u) == rcOK { // newer drivers answer for instances
			p.Metrics[device.Util], p.Metrics[device.MemBandwidth] = float64(u.GPU), float64(u.Memory)
		}
		p.Procs = append(procs(mh, lib.compute), procs(mh, lib.graphics)...)
		out = append(out, p)
	}
	return out
}

func procs(h uintptr, call func(uintptr, *uint32, *processInfo) int32) []device.Process {
	if call == nil {
		return nil
	}
	n := uint32(64)
	buf := make([]processInfo, n)
	rc := call(h, &n, &buf[0])
	if rc == rcInsufficient {
		buf = make([]processInfo, n+16)
		n = uint32(len(buf))
		rc = call(h, &n, &buf[0])
	}
	if rc != rcOK {
		return nil
	}
	out := make([]device.Process, 0, n)
	for _, p := range buf[:n] {
		proc := device.Process{PID: int(p.PID), Name: provider.ProcName(int(p.PID)), Metrics: device.Metrics{}}
		if p.Mem != memNotKnown {
			proc.Metrics[device.MemUsed] = float64(p.Mem)
		}
		out = append(out, proc)
	}
	return out
}
