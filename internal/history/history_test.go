package history

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

func dev(util float64) device.Device {
	d := device.New(device.AMD, 0, "x", "", "")
	d.Metrics[device.Util] = util
	return d
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Keep: time.Hour, Resolution: time.Second, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Minute)
	s.Record(t0, []device.Device{dev(10)})
	s.Record(t0.Add(500*time.Millisecond), []device.Device{dev(99)}) // below resolution: dropped
	s.Record(t0.Add(2*time.Second), []device.Device{dev(20)})
	s.Record(t0.Add(4*time.Second), []device.Device{dev(30)})
	s.Close()

	// Corrupt the middle line and tear the tail: both must be skipped.
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	b, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines %v", lines)
	}
	lines[1] = strings.Replace(lines[1], " 20 ", " 21 ", 1)
	if err := os.WriteFile(files[0], []byte(strings.Join(lines, "\n")+"\n12345 amd-0 1 2"), 0o644); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(Options{Keep: time.Hour, Resolution: time.Second, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got := s2.Recent("amd-0", device.Util, 10)
	if len(got) != 2 || got[0] != 10 || got[1] != 30 {
		t.Fatalf("recent = %v (corrupt line must be dropped)", got)
	}
	if v, ok := s2.At(t0.Add(time.Second))["amd-0"].Get(device.Util); !ok || v != 10 {
		t.Fatalf("at = %v %v", v, ok)
	}
	if temp := s2.Recent("amd-0", device.Temp, 1); !math.IsNaN(temp[0]) {
		t.Fatalf("missing metric must be NaN, got %v", temp)
	}
	if b := s2.Between("amd-0", t0.Add(3*time.Second), t0.Add(10*time.Second)); len(b) != 1 {
		t.Fatalf("between %v", b)
	}
}

func TestLock(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(Options{Keep: time.Hour, Resolution: time.Second, Dir: dir})
	if err != nil || a.Warning != "" {
		t.Fatal(err, a.Warning)
	}
	b, err := Open(Options{Keep: time.Hour, Resolution: time.Second, Dir: dir})
	if err != nil || b.Warning == "" {
		t.Fatalf("second opener must fall back to memory: %v %q", err, b.Warning)
	}
	a.Close()
	c, _ := Open(Options{Keep: time.Hour, Resolution: time.Second, Dir: dir})
	if c.Warning != "" {
		t.Fatal("lock must be released on close")
	}
	c.Close()
}

func TestDiskCap(t *testing.T) {
	dir := t.TempDir()
	for _, h := range []string{"2026010100", "2026010101"} {
		if err := os.WriteFile(filepath.Join(dir, h+".log"), make([]byte, 1000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Store{o: Options{Keep: 100 * 365 * 24 * time.Hour, Dir: dir, MaxDisk: 1500}}
	s.prune(time.Now())
	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) != 1 || !strings.HasSuffix(files[0], "2026010101.log") {
		t.Fatalf("files %v", files)
	}
}

func TestKeep(t *testing.T) {
	s, _ := Open(Options{Keep: time.Minute, Resolution: time.Second})
	now := time.Now()
	s.Record(now.Add(-2*time.Minute), []device.Device{dev(1)})
	s.Record(now, []device.Device{dev(2)})
	if got := s.Recent("amd-0", device.Util, 10); len(got) != 1 || got[0] != 2 {
		t.Fatalf("old points must be trimmed: %v", got)
	}
}

func TestThrottleBurstSurvives(t *testing.T) {
	s, _ := Open(Options{Keep: time.Hour, Resolution: 10 * time.Second})
	t0 := time.Now().Add(-time.Minute)
	d := dev(1)
	d.Metrics[device.Throttle] = 0
	s.Record(t0, []device.Device{d})
	d.Metrics[device.Throttle] = device.ThrottleThermal
	s.Record(t0.Add(time.Second), []device.Device{d}) // below resolution: not a point
	d.Metrics[device.Throttle] = 0
	s.Record(t0.Add(11*time.Second), []device.Device{d})
	ps := s.Series("amd-0")
	if len(ps) != 2 {
		t.Fatalf("points %d", len(ps))
	}
	if v, _ := ps[1].Get(device.Throttle); int(v)&device.ThrottleThermal == 0 {
		t.Fatalf("burst lost: %v", v)
	}
}
