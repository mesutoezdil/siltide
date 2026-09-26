package health

import (
	"testing"

	"github.com/moezdil/siltide/internal/device"
)

func TestScore(t *testing.T) {
	d := device.New(device.NVIDIA, 0, "x", "", "")
	o := Options{TempWarn: 85}
	if s, n := Score(d, o); s != 50 || len(n) != 1 {
		t.Fatalf("empty device = %d %v", s, n)
	}
	d.Metrics[device.Util] = 10
	d.Metrics[device.Throttle] = device.ThrottleThermal | device.ThrottlePowerCap
	d.Metrics[device.Temp] = 90
	d.Metrics[device.PCIeWidth], d.Metrics[device.PCIeMaxWidth] = 8, 16
	if s, n := Score(d, o); s != 55 || len(n) != 4 {
		t.Fatalf("got %d %v", s, n)
	}
	d.Metrics[device.LinksActive], d.Metrics[device.LinksTotal] = 17, 18
	d.Links = []device.Link{{Errors: 3}}
	if s, _ := Score(d, o); s != 40 {
		t.Fatalf("links: %d", s)
	}
	o.Xids, o.XidWorst = []int{79}, "critical"
	if s, n := Score(d, o); s != 20 || n[0] != "critical Xid in the last 10 minutes: 79" {
		t.Fatalf("xid: %d %v", s, n)
	}
	d.Metrics[device.EccUncorrected] = 3
	d.Metrics[device.Throttle] = device.ThrottleHW | device.ThrottleThermal | device.ThrottlePowerCap
	if s, _ := Score(d, o); s != 0 {
		t.Fatalf("floor at 0, got %d", s)
	}
	if Band(92) != "healthy" || Band(60) != "degraded" || Band(10) != "critical" {
		t.Fatal("bands")
	}
}
