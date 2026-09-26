package events

import (
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

func TestDetector(t *testing.T) {
	d := New(Thresholds{TempWarn: 85}, 100)
	dev := device.New(device.NVIDIA, 0, "x", "", "")
	dev.Metrics[device.Temp] = 70
	dev.Metrics[device.EccUncorrected] = 0
	dev.Procs = []device.Process{{PID: 1, Name: "python"}}
	now := time.Now()
	d.Observe(now, []device.Device{dev}, nil)
	if len(d.Log()) != 0 || len(d.Alerts()) != 0 {
		t.Fatalf("first snapshot must be quiet: %v", d.Log())
	}

	// One hot sample is noise, no alert yet; 2 in a row raise it.
	dev.Metrics[device.Temp] = 90
	dev.Metrics[device.EccUncorrected] = 2
	dev.Procs = nil
	d.Observe(now, []device.Device{dev}, map[string]string{"nvidia": ""})
	if len(d.Alerts()) != 0 {
		t.Fatalf("debounce: %+v", d.Alerts())
	}
	d.Observe(now, []device.Device{dev}, nil)
	al := d.Alerts()
	if len(al) != 1 || al[0].Kind != "thermal" {
		t.Fatalf("alerts %+v", al)
	}
	var kinds []string
	for _, e := range d.Log() {
		kinds = append(kinds, e.Kind)
	}
	if kinds[0] != "ecc_uncorrected" || kinds[1] != "process" || kinds[2] != "thermal" {
		t.Fatalf("log kinds %v", kinds)
	}

	dev.Metrics[device.Temp] = 60
	d.Observe(now, nil, nil) // device vanished
	if len(d.Alerts()) != 0 {
		t.Fatal("alerts of a vanished device must clear")
	}
	if lg := d.Log(); lg[len(lg)-1].Message != "vanished: x" {
		t.Fatalf("log %+v", lg)
	}
}

func TestRules(t *testing.T) {
	r, err := ParseRule("", "util < 10", 2*time.Minute, "allocated", "")
	if err != nil || r.Name != "util < 10" || r.Severity != Warning || r.Value != 10 {
		t.Fatalf("%+v %v", r, err)
	}
	if m, err := ParseRule("big", "mem_used > 70G", 0, "", "critical"); err != nil || m.Value != 70<<30 {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := ParseRule("", "util 10", 0, "", ""); err == nil {
		t.Fatal("bad clause accepted")
	}
	d := New(Thresholds{TempWarn: 85, Rules: []Rule{r}}, 100)
	dev := device.New(device.NVIDIA, 0, "x", "", "")
	dev.Metrics[device.Util] = 3
	dev.Procs = []device.Process{{PID: 1}}
	t0 := time.Now()
	d.Observe(t0, []device.Device{dev}, nil)
	d.Observe(t0.Add(time.Minute), []device.Device{dev}, nil)
	if len(d.Alerts()) != 0 {
		t.Fatal("rule must wait for its duration")
	}
	d.Observe(t0.Add(3*time.Minute), []device.Device{dev}, nil)
	d.Observe(t0.Add(3*time.Minute+time.Second), []device.Device{dev}, nil)
	al := d.Alerts()
	if len(al) != 1 || al[0].Kind != "rule:util < 10" {
		t.Fatalf("alerts %+v", al)
	}
	dev.Procs = nil // not allocated any more: rule no longer applies
	d.Observe(t0.Add(4*time.Minute), []device.Device{dev}, nil)
	d.Observe(t0.Add(4*time.Minute+time.Second), []device.Device{dev}, nil)
	if len(d.Alerts()) != 0 {
		t.Fatal("rule must clear")
	}
}

func TestXidsAndLinks(t *testing.T) {
	d := New(Thresholds{TempWarn: 85}, 100)
	now := time.Now()
	d.External(now, Critical, "xid", "nvidia-0", "0", "Xid 79", 79)
	d.External(now.Add(-time.Hour), Warning, "xid", "nvidia-0", "0", "Xid 13", 13)
	codes, worst := d.RecentXids("nvidia-0", now, 10*time.Minute)
	if len(codes) != 1 || codes[0] != 79 || worst != Critical {
		t.Fatalf("%v %s", codes, worst)
	}
	dev := device.New(device.NVIDIA, 0, "x", "", "")
	dev.Links = []device.Link{{Index: 0, Active: true}, {Index: 1, Active: true}}
	d.Observe(now, []device.Device{dev}, nil)
	dev.Links[1].Active = false
	dev.Links[0].Errors = 5
	d.Observe(now, []device.Device{dev}, nil)
	lg := d.Log()
	if len(lg) < 2 || lg[len(lg)-2].Message != "link 0: 5 new error(s)" || lg[len(lg)-1].Message != "link 1 went down" {
		t.Fatalf("log %+v", lg)
	}
}
