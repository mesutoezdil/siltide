package smi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

const mib = 1 << 20

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// want is what every parser test checks. -1 means the metric must be
// absent; a zero memTotal means both memory metrics must be absent.
type want struct {
	idx               int
	name, id          string
	util, temp, power float64
	memUsed, memTotal float64
}

func check(t *testing.T, got []device.Device, ws ...want) {
	t.Helper()
	if len(got) != len(ws) {
		t.Fatalf("got %d devices, want %d: %+v", len(got), len(ws), got)
	}
	for i, w := range ws {
		d := got[i]
		if d.Index != w.idx || d.Name != w.name || d.ID != string(d.Vendor)+"-"+w.id {
			t.Errorf("#%d identity = %d %q %q, want %d %q %q", i, d.Index, d.Name, d.ID, w.idx, w.name, w.id)
		}
		metric := func(k device.Metric, want float64) {
			v, ok := d.Metrics.Get(k)
			if want < 0 && ok {
				t.Errorf("#%d %s = %v, want absent", i, k, v)
			}
			if want >= 0 && (!ok || v != want) {
				t.Errorf("#%d %s = %v (ok=%v), want %v", i, k, v, ok, want)
			}
		}
		metric(device.Util, w.util)
		metric(device.Temp, w.temp)
		metric(device.Power, w.power)
		if w.memTotal == 0 {
			metric(device.MemTotal, -1)
		} else {
			metric(device.MemUsed, w.memUsed)
			metric(device.MemTotal, w.memTotal)
		}
	}
}

func TestNpuSmi(t *testing.T) {
	check(t, parseNpuSmi(fixture(t, "npusmi.txt")),
		want{0, "Ascend 910B3", "0000:C1:00.0", 0, 37, 92.9, 3379 * mib, 65536 * mib},
		want{1, "Ascend 910B3", "0000:C2:00.0", 87, 45, 250.1, 60000 * mib, 65536 * mib},
	)
	check(t, parseNpuSmi(fixture(t, "npusmi_310.txt")),
		want{0, "Ascend 310P3", "0000:01:00.0", 0, 41, -1, 1003 * mib, 21534 * mib},
	)
}

func TestCnmon(t *testing.T) {
	check(t, parseCnmon(fixture(t, "cnmon.txt")),
		want{0, "Cambricon MLU370-X4", "0", 0, 32, 30, 0, 23308 * mib},
	)
	withProc := fixture(t, "cnmon.txt") + "|  0     /   12345   python train.py                          1024 MiB          |\n"
	got := parseCnmon(withProc)
	if len(got[0].Procs) != 1 || got[0].Procs[0].PID != 12345 || got[0].Procs[0].Name != "python" || got[0].Procs[0].Metrics[device.MemUsed] != 1024*mib {
		t.Fatalf("procs %+v", got[0].Procs)
	}
}

func TestNpuSmiProcs(t *testing.T) {
	got := parseNpuSmi(fixture(t, "npusmi.txt"))
	if len(got[1].Procs) != 1 || got[1].Procs[0].PID != 812345 || got[1].Procs[0].Metrics[device.MemUsed] != 56621*mib || len(got[0].Procs) != 0 {
		t.Fatalf("procs %+v / %+v", got[0].Procs, got[1].Procs)
	}
}

func TestDRMProcs(t *testing.T) {
	proc := t.TempDir()
	fd := func(pid, n, body string) {
		p := filepath.Join(proc, pid, "fdinfo", n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fd("100", "5", "pos: 0\ndrm-driver: amdgpu\ndrm-client-id: 7\ndrm-pdev: 0000:03:00.0\ndrm-memory-vram: 2048 KiB\ndrm-engine-gfx: 1000000000 ns\n")
	fd("100", "6", "drm-driver: amdgpu\ndrm-client-id: 7\ndrm-pdev: 0000:03:00.0\ndrm-memory-vram: 2048 KiB\n") // same client, dup fd
	fd("100", "7", "not drm\n")
	if err := os.WriteFile(filepath.Join(proc, "100", "comm"), []byte("blender\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drmEngine.prev, drmEngine.at = map[string]float64{}, time.Time{}
	first := drmProcs(proc)
	ps := first["0000:03:00.0"]
	if len(ps) != 1 || ps[0].PID != 100 || ps[0].Name != "blender" || ps[0].Metrics[device.MemUsed] != 2048*1024 {
		t.Fatalf("first %+v", ps)
	}
	if _, ok := ps[0].Metrics.Get(device.Util); ok {
		t.Fatal("util needs two samples")
	}
	fd("100", "5", "drm-client-id: 7\ndrm-pdev: 0000:03:00.0\ndrm-memory-vram: 2048 KiB\ndrm-engine-gfx: 1500000000 ns\n")
	drmEngine.at = drmEngine.at.Add(-time.Second)
	second := drmProcs(proc)["0000:03:00.0"]
	if u := second[0].Metrics[device.Util]; u < 49 || u > 51 {
		t.Fatalf("util %v", u)
	}
}

func TestEfsmi(t *testing.T) {
	got := parseEfsmi(fixture(t, "efsmi.txt"))
	if len(got) != 10 {
		t.Fatalf("got %d devices, want 10", len(got))
	}
	check(t, got[:1], want{0, "Enflame S60", "TPUH90200803", 0, 35, 99, 38934 * mib, 42976 * mib})
	if got[0].Bus != "0000:0d:00.0" || got[0].Metrics[device.PowerCap] != 300 {
		t.Errorf("bus %q cap %v", got[0].Bus, got[0].Metrics[device.PowerCap])
	}
	old := parseEfsmi(fixture(t, "efsmi_old.txt"))
	check(t, old[:1], want{0, "Enflame GCU", "0", 0, 39, -1, 1129 * mib, 42976 * mib})
}

func TestHySmi(t *testing.T) {
	got, err := parseHySmi([]byte(fixture(t, "hysmi.json")))
	if err != nil {
		t.Fatal(err)
	}
	check(t, got, want{0, "Hygon DCU xxxx", "7100fa8866904061", 0, 41, 80, 2 * mib, 65520 * mib})

	// Older `hy-smi`: HCU key, byte units, numbers not strings.
	got, err = parseHySmi([]byte(`{"card1":{"HCU use (%)":7,"vram Total Memory (B)":"1024","vram Total Used Memory (B)":"512","Temperature (Sensor core) (C)":"50.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	check(t, got, want{1, "Hygon DCU", "1", 7, 50, -1, 512, 1024})
}

func TestIxsmi(t *testing.T) {
	got, err := parseIxsmi([]byte(fixture(t, "ixsmi.xml")))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	check(t, got[:1], want{0, "Iluvatar MR-V100", "GPU-1ac807aa-cbcd-5579-8591-d59d436d6eca", 0, 44, 37, 27780 * mib, 32768 * mib})
	if got[0].Bus != "00000000:0C:00.0" || got[1].Index != 1 {
		t.Errorf("bus %q, second index %d", got[0].Bus, got[1].Index)
	}
}

func TestXpuSmi(t *testing.T) {
	check(t, parseXpuSmi(fixture(t, "xpusmi.txt")+"garbage line\n"),
		want{0, "Kunlunxin P800 OAM", "02K15K624CV00304", 0, 36, 86, 90440 * mib, 98304 * mib},
		want{1, "Kunlunxin P800 OAM", "02K15K624CV0030V", 0, 39, 86, 90440 * mib, 98304 * mib},
	)
}

func TestMxSmi(t *testing.T) {
	check(t, parseMxSmi(fixture(t, "mxsmi.txt")),
		want{0, "MetaX C500", "0000:0e:00.0", 0, 35, 57, 826 * mib, 65536 * mib},
	)
	check(t, parseMxSmi(fixture(t, "mxsmi_old.txt")),
		want{0, "MetaX N260", "0000:01:00.0", 0, 37, 29, 666 * mib, 65536 * mib},
	)
}

func TestMthreadsGmi(t *testing.T) {
	check(t, parseMthreadsGmi(fixture(t, "mthreadsgmi.txt")),
		want{0, "Moore Threads MTT S80", "00000000:01:00.0", 0, 59, -1, 3419 * mib, 16384 * mib},
	)
	check(t, parseMthreadsGmi(fixture(t, "mthreadsgmi_new.txt")),
		want{0, "Moore Threads MTT S5000", "00000000:18:00.0", 0, 48, -1, 0, 81920 * mib},
	)
}

func TestQueryCSV(t *testing.T) {
	check(t, parseQueryCSV(device.Biren, "0, Biren 106M, GPU-1, 00000000:01:00.0, 12, 1024, 32512, 40\n1, Biren 106M, GPU-2, 00000000:02:00.0, [N/A], 0, 32512, [N/A]\n"),
		want{0, "Biren 106M", "GPU-1", 12, 40, -1, 1024 * mib, 32512 * mib},
		want{1, "Biren 106M", "GPU-2", -1, -1, -1, 0, 32512 * mib},
	)
}

func TestNeuronApply(t *testing.T) {
	n := &neuron{started: true, devs: []neuronDev{{Index: 0, BDF: "cc:00.0", Memory: 96 << 30, Cores: []int{0, 1}}}}
	r, _ := n.read(context.Background(), "")
	if len(r) != 1 || r[0].Name != "AWS Neuron device" {
		t.Fatalf("before the first report: %+v", r)
	}
	if _, ok := r[0].Metrics.Get(device.Util); ok {
		t.Error("util before the first report must be absent")
	}
	var rep neuronReport
	if err := json.Unmarshal([]byte(`{"neuron_runtime_data":[{"report":{
		"neuroncore_counters":{"neuroncores_in_use":{"0":{"neuroncore_utilization":42,"v3d":{}}}},
		"memory_used":{"neuron_runtime_used_bytes":{"usage_breakdown":{"neuroncore_memory_usage":{"1":{"constants":10,"tensors":5}}}}}}}],
		"neuron_hardware_info":{"neuron_device_type":"trainium2"}}`), &rep); err != nil {
		t.Fatal(err)
	}
	n.apply(rep)
	r, _ = n.read(context.Background(), "")
	check(t, r, want{0, "AWS Trainium2", "cc:00.0", 21, -1, -1, 15, 96 << 30})
}

func TestDRM(t *testing.T) {
	root := t.TempDir()
	write := func(p, v string) {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("card0/device/vendor", "0x1002")
	write("card0/device/device", "0x744c")
	write("card0/device/gpu_busy_percent", "42")
	write("card0/device/mem_info_vram_used", "1024")
	write("card0/device/mem_info_vram_total", "4096")
	write("card0/device/hwmon/hwmon3/temp1_input", "55000")
	write("card0/device/hwmon/hwmon3/power1_input", "120000000")
	write("card0-DP-1/status", "connected")
	write("card1/device/vendor", "0x10de") // NVIDIA: NVML's job
	write("card2/device/vendor", "0x8086")

	amd := readDRM(device.AMD, root, "0x1002", "AMD")
	check(t, amd, want{0, amd[0].Name, "device", 42, 55, 120, 1024, 4096})
	intel := readDRM(device.Intel, root, "0x8086", "Intel")
	check(t, intel, want{2, intel[0].Name, "device", -1, -1, -1, 0, 0})
}

func TestPCINames(t *testing.T) {
	f := filepath.Join(t.TempDir(), "pci.ids")
	if err := os.WriteFile(f, []byte("# comment\n1002  Advanced Micro Devices, Inc. [AMD/ATI]\n\t744c  Navi 31 [Radeon RX 7900 XT/7900 XTX]\n\t\t1002 0e3b  sub\n10de  NVIDIA\n\t2330  H100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadPCINames([]string{"/nonexistent", f})
	if got["1002:744c"] != "Navi 31 [Radeon RX 7900 XT/7900 XTX]" || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestSetBytes(t *testing.T) {
	m := device.Metrics{}
	setBytes(m, device.MemUsed, "3 GiB", "mib")
	setBytes(m, device.MemTotal, "N/A", "mib")
	if m[device.MemUsed] != 3<<30 {
		t.Errorf("GiB = %v", m[device.MemUsed])
	}
	if _, ok := m[device.MemTotal]; ok {
		t.Error("N/A must stay absent")
	}
}

// TestCnmonBlankProcessName covers a cnmon process row whose command column
// came back empty, which the table still prints as a row of spaces.
func TestCnmonBlankProcessName(t *testing.T) {
	const table = `
| 0     /   MLU370-X4    v1.1.6 | On          v4.20.11 | 0%          N/A       |
|  0%   32C         30 W/ 150 W |  1024 MiB/ 23308 MiB | 10240 MiB/1048576 MiB |
|  0     /   12345                                                    512 MiB |
`
	devs := parseCnmon(table)
	if len(devs) != 1 {
		t.Fatalf("parsed %d devices, want 1", len(devs))
	}
	for _, p := range devs[0].Procs {
		if p.PID != 12345 {
			t.Errorf("pid %d", p.PID)
		}
		if p.Name != "" && strings.TrimSpace(p.Name) == "" {
			t.Errorf("process name %q should be empty, not blank", p.Name)
		}
	}
}
