//go:build linux

package nvidia

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

// TestFakeNVML builds `testdata/fakenvml.c` into `libnvidia-ml.so.1` and
// runs the real bindings against it: every struct siltide passes through
// purego must match the C ABI or the values come back wrong. Skipped without
// a C compiler.
func TestFakeNVML(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	so := filepath.Join(dir, "libnvidia-ml.so.1")
	out, err := exec.Command(cc, "-shared", "-fPIC", "-o", so, "testdata/fakenvml.c").CombinedOutput()
	if err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	t.Setenv("LD_LIBRARY_PATH", dir+":"+os.Getenv("LD_LIBRARY_PATH"))
	libName = so // dlopen by absolute path: LD_LIBRARY_PATH is read at process start

	p := Provider()
	if err := p.Detect(); err != nil {
		t.Fatal(err)
	}
	devs, err := p.Read(context.Background())
	if err != nil || len(devs) != 2 {
		t.Fatalf("%v %d devices", err, len(devs))
	}
	d0, d1 := devs[0], devs[1]
	want := map[device.Metric]float64{
		device.Util: 97, device.MemBandwidth: 60, device.MemUsed: 60 << 30, device.MemTotal: 80 << 30, device.Temp: 68, device.Power: 640,
		device.PowerCap: 700, device.ClockCore: 1980, device.ClockMem: 2619, device.EccCorrected: 3, device.EccUncorrected: 0,
		device.PCIeGen: 5, device.PCIeWidth: 16, device.PCIeMaxWidth: 16, device.PCIeTx: 4e9, device.PCIeRx: 1.2e10,
		device.LinksTotal: 18, device.LinksActive: 18, device.RemappedRows: 0, device.RetiredPages: 2, device.PCIeReplays: 0,
		device.Energy: 5e6, device.PState: 0, device.TempShutdown: 95, device.TempSlowdown: 90, device.MemTemp: 55, device.ViolationTherm: 0,
	}
	for k, v := range want {
		if got, ok := d0.Metrics.Get(k); !ok || got != v {
			t.Errorf("device 0 %s = %v (ok=%v), want %v", k, got, ok, v)
		}
	}
	if _, ok := d0.Metrics.Get(device.Fan); ok {
		t.Error("unsupported fan must be absent")
	}
	if d0.ID != "nvidia-GPU-fake-0000" || d0.Bus != "00000000:0A:00.0" || d0.Name != "Fake H100 80GB" {
		t.Errorf("identity %+v", d0)
	}
	if len(d0.Links) != 18 || d0.Links[0].Peer != "00000000:0B:00.0" || d0.Links[0].Version != "4" || d0.Links[0].Tx != 2048*1024 {
		t.Errorf("links %+v", d0.Links[:1])
	}
	if len(d0.Procs) != 1 || d0.Procs[0].PID != os.Getpid() || d0.Procs[0].Metrics[device.MemUsed] != 55<<30 || d0.Procs[0].Metrics[device.SMUtil] != 95 {
		t.Errorf("procs %+v", d0.Procs)
	}
	if d1.Metrics[device.Throttle] != device.ThrottleThermal || d1.Metrics[device.PCIeWidth] != 8 || d1.Metrics[device.EccUncorrected] != 1 ||
		d1.Metrics[device.LinksActive] != 17 || d1.Metrics[device.PCIeReplays] != 23 || d1.Metrics[device.ViolationTherm] != 25 || d1.Links[3].Errors != 12 {
		t.Errorf("device 1 %+v", d1.Metrics)
	}

	got := make(chan provider.Event, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go p.Events(ctx, func(e provider.Event) {
		select {
		case got <- e:
		default:
		}
	})
	select {
	case e := <-got:
		if e.Kind != "xid" || e.Code != 79 || e.Severity != "critical" || e.DeviceID != "nvidia-GPU-fake-0001" {
			t.Errorf("event %+v", e)
		}
	case <-ctx.Done():
		t.Error("no Xid event arrived")
	}
}
