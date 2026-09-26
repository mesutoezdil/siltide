package derive

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

func TestApply(t *testing.T) {
	tr := New(Thresholds{IdleUtil: 5, BusyUtil: 80, IdleAfter: time.Minute, OutlierDelta: 20})
	var devs []device.Device
	for i, u := range []float64{95, 92, 30, 2} {
		d := device.New(device.NVIDIA, i, "H100", "", "")
		d.Metrics[device.Util] = u
		d.Procs = []device.Process{{PID: 1}}
		devs = append(devs, d)
	}
	devs = append(devs, device.New(device.AMD, 0, "dead", "", ""))
	now := time.Now()
	tr.Apply(now, devs)
	states := []device.State{device.StateBusy, device.StateBusy, device.StateActive, device.StateIdle, device.StateDown}
	for i, s := range states {
		if devs[i].State != s {
			t.Errorf("#%d state %s, want %s", i, devs[i].State, s)
		}
	}
	if !devs[2].Outlier || !devs[3].Outlier || devs[0].Outlier {
		t.Errorf("outliers: %v %v %v", devs[0].Outlier, devs[2].Outlier, devs[3].Outlier)
	}
	if devs[3].IdleAlloc {
		t.Error("idle-allocated needs idle_after to pass")
	}
	tr.Apply(now.Add(2*time.Minute), devs)
	if !devs[3].IdleAlloc {
		t.Error("idle-allocated after idle_after")
	}
	f := Summarize(devs)
	if f.Busy != 2 || f.Active != 1 || f.Idle != 1 || f.Down != 1 || f.Allocated != 4 || f.IdleAlloc != 1 || f.Outliers != 2 {
		t.Errorf("fleet %+v", f)
	}
	if want := (5 + 8 + 70 + 98) / 100.0; f.Unused < want-1e-9 || f.Unused > want+1e-9 {
		t.Errorf("unused %v want %v", f.Unused, want)
	}
}

func TestFleetJSON(t *testing.T) {
	f := Summarize(nil)
	b, err := json.Marshal(f)
	if err != nil || !strings.Contains(string(b), `"avg_util":null`) {
		t.Fatalf("%s %v", b, err)
	}
	var back Fleet
	if err := json.Unmarshal(b, &back); err != nil || !math.IsNaN(back.AvgUtil) {
		t.Fatalf("round trip: %+v %v", back, err)
	}
}
