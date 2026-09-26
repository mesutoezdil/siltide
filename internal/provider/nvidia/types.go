package nvidia

import "github.com/moezdil/siltide/internal/device"

// C ABI mirrors of the NVML (NVIDIA Management Library) structs siltide passes
// to the driver. They are platform independent so the layout tests run
// everywhere.

type memory struct{ Total, Free, Used uint64 }

// memoryV2 mirrors nvmlMemory_v2_t. Newer drivers count a reserved region
// (row-remap and similar bookkeeping) as part of memory.Used in the plain
// nvmlMemory_t struct, so Total keeps equaling Used+Free; the v2 call keeps
// Reserved apart, which is the number `nvidia-smi` shows as used. Version
// must be set to sizeof(nvmlMemory_v2_t)|(2<<24) before the call.
type memoryV2 struct {
	Version                     uint32
	_                           uint32 // padding to align the uint64 fields
	Total, Reserved, Free, Used uint64
}

type utilization struct{ GPU, Memory uint32 }

// pciInfo mirrors nvmlPciInfo_t (v3): 16-byte legacy id, 5 u32s, 32-byte id.
type pciInfo struct {
	Legacy      [16]byte
	Domain      uint32
	Bus         uint32
	Device      uint32
	DeviceID    uint32
	SubsystemID uint32
	BusID       [32]byte
}

// processInfo mirrors nvmlProcessInfo_t as used by the v2/v3 process calls.
type processInfo struct {
	PID uint32
	Mem uint64 // ^0 when the driver cannot attribute memory
	GI  uint32
	CI  uint32
}

// processUtil mirrors nvmlProcessUtilizationSample_t.
type processUtil struct {
	PID       uint32
	Timestamp uint64
	SM        uint32
	Mem       uint32
	Enc       uint32
	Dec       uint32
}

// violation mirrors nvmlViolationTime_t (reference and violation time in ns).
type violation struct{ Reference, Violation uint64 }

// eventData mirrors nvmlEventData_t.
type eventData struct {
	Device uintptr
	Type   uint64
	Data   uint64
	GI     uint32
	CI     uint32
}

// fieldValue mirrors nvmlFieldValue_t.
type fieldValue struct {
	ID        uint32
	Scope     uint32
	Timestamp int64
	Latency   int64
	ValueType uint32
	Return    uint32
	Value     uint64
}

// throttleBits maps NVML clock event reasons onto device.Throttle* bits.
func throttleBits(r uint64) int {
	const (
		idle       = 0x1
		appClocks  = 0x2
		swPower    = 0x4
		hwSlowdown = 0x8
		syncBoost  = 0x10
		swThermal  = 0x20
		hwThermal  = 0x40
		hwBrake    = 0x80
		display    = 0x100
	)
	b := 0
	if r&idle != 0 {
		b |= device.ThrottleIdle
	}
	if r&(swPower|hwBrake) != 0 {
		b |= device.ThrottlePowerCap
	}
	if r&(swThermal|hwThermal) != 0 {
		b |= device.ThrottleThermal
	}
	if r&hwSlowdown != 0 {
		b |= device.ThrottleHW
	}
	if r&(appClocks|syncBoost|display) != 0 {
		b |= device.ThrottleOther
	}
	return b
}
