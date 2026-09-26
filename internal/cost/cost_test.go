package cost

import (
	"testing"

	"github.com/moezdil/siltide/internal/device"
)

func TestSummarize(t *testing.T) {
	tb := Table{PerHour: map[string]float64{"h100": 4, "h100 80gb": 5, "mi300": 3}}
	busy := device.New(device.NVIDIA, 0, "NVIDIA H100 80GB", "", "")
	busy.Metrics[device.Util] = 50
	busy.Procs = []device.Process{{PID: 1}}
	idle := device.New(device.AMD, 0, "AMD MI300X", "", "")
	free := device.New(device.NVIDIA, 1, "NVIDIA H100 PCIe", "", "")
	free.Metrics[device.Util] = 0
	unpriced := device.New(device.Ascend, 0, "Ascend 910B", "", "")
	if p := tb.Price(busy); p != 5 {
		t.Fatalf("longest match: %v", p)
	}
	s := tb.Summarize([]device.Device{busy, idle, free, unpriced})
	if s.Priced != 3 || s.SpendPerHour != 12 || s.WastePerHour != 2.5 || s.IdlePerHour != 7 {
		t.Fatalf("%+v", s)
	}
}
