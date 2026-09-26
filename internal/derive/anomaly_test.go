package derive

import (
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/history"
)

func TestAnomalies(t *testing.T) {
	h, _ := history.Open(history.Options{Keep: time.Hour, Resolution: time.Second})
	now := time.Now()
	collapse := device.New(device.NVIDIA, 0, "a", "", "")
	collapse.Procs = []device.Process{{PID: 1}}
	leak := device.New(device.NVIDIA, 1, "b", "", "")
	leak.Metrics[device.MemTotal] = 100
	creep := device.New(device.NVIDIA, 2, "c", "", "")
	for i := 0; i < 60; i++ {
		at := now.Add(time.Duration(i-60) * time.Second)
		collapse.Metrics[device.Util] = 90
		if i >= 50 {
			collapse.Metrics[device.Util] = 5
		}
		leak.Metrics[device.MemUsed] = float64(40 + i) // fills in ~40 more samples
		creep.Metrics[device.Util] = 50
		creep.Metrics[device.Temp] = float64(60 + i/3)
		h.Record(at, []device.Device{collapse, leak, creep})
	}
	leak.Metrics[device.MemUsed] = 99
	got := Anomalies(h, []device.Device{collapse, leak, creep}, now, time.Minute)
	kinds := map[string]bool{}
	for _, a := range got {
		kinds[a.Kind] = true
	}
	if !kinds["util-collapse"] || !kinds["memory-leak"] || !kinds["temp-creep"] {
		t.Fatalf("got %+v", got)
	}
}
