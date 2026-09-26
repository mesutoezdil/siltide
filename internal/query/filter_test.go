package query

import (
	"testing"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/events"
)

func testDevices() []device.Device {
	hot := device.New(device.NVIDIA, 0, "H100", "hot", "")
	hot.Health = 80
	hot.Metrics = device.Metrics{device.Util: 92, device.Temp: 74, device.MemUsed: 60 << 30, device.MemTotal: 80 << 30}
	hot.Procs = []device.Process{{
		PID: 4242, Name: "python", User: "alice", Namespace: "ml", Pod: "train-0",
		Metrics: device.Metrics{device.MemUsed: 40 << 30, device.Util: 88},
	}}

	cool := device.New(device.AMD, 1, "MI300", "cool", "")
	cool.Health = 100
	cool.Metrics = device.Metrics{device.Util: 12, device.Temp: 41, device.MemUsed: 8 << 30, device.MemTotal: 128 << 30}
	cool.Procs = []device.Process{{
		PID: 77, Name: "jupyter", User: "bob", Namespace: "research", Pod: "nb-1",
		Metrics: device.Metrics{device.MemUsed: 4 << 30},
	}}

	bare := device.New(device.Intel, 2, "Max 1550", "bare", "")
	bare.Health = 100
	return []device.Device{hot, cool, bare}
}

// matched lists the indexes of the devices a filter keeps.
func matched(t *testing.T, q string) []int {
	t.Helper()
	f := ParseFilter(q)
	var out []int
	for _, d := range testDevices() {
		if f.Device(d) {
			out = append(out, d.Index)
		}
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilterComparisons(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{"util>80", []int{0}},
		{"util<80", []int{1}},
		{"util>=92", []int{0}},
		{"temp>=70", []int{0}},
		{"temp<=41", []int{1}},
		{"health<100", []int{0}},
		{"health==100", []int{1, 2}},
		{"health=100", []int{1, 2}},
		{"health!=100", []int{0}},
		{"mem>50", []int{0}},
		{"mem<50", []int{1}},
		{"mem_used>8G", []int{0}},
		{"mem_used>=8G", []int{0, 1}},
		{"procs>0", []int{0, 1}},
		{"dev>0", []int{1, 2}},
		{"util>80 vendor:nvidia", []int{0}},
		{"util>80 vendor:amd", nil},
		{"fan>10", nil},          // no device reports a fan
		{"util>200", nil},        // in range, nothing that high
		{"util>=0", []int{0, 1}}, // the bare device reports no util at all
	} {
		if got := matched(t, tc.query); !equal(got, tc.want) {
			t.Errorf("%q matched %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestFilterNegation(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{"!vendor:nvidia", []int{1, 2}},
		{"vendor:nvidia", []int{0}},
		{"!ns:ml", []int{1, 2}},
		{"!user:bob", []int{0, 2}},
		{"!python", []int{1, 2}},
		{"!h100", []int{1, 2}},
		{"!util>80", []int{1}},
		{"!util<80", []int{0}},
		{"!vendor:nvidia !vendor:amd", []int{2}},
		{"!vendor:nvidia util<80", []int{1}},
	} {
		if got := matched(t, tc.query); !equal(got, tc.want) {
			t.Errorf("%q matched %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestFilterKeepsWordAndKeySyntax(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{"", []int{0, 1, 2}},
		{"h100", []int{0}},
		{"dev:1", []int{1}},
		{"gpu:1", []int{1}},
		{"ns:ml", []int{0}},
		{"namespace:research", []int{1}},
		{"user:alice", []int{0}},
		{"python", []int{0}},
	} {
		if got := matched(t, tc.query); !equal(got, tc.want) {
			t.Errorf("%q matched %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestFilterProcessNumbers(t *testing.T) {
	devs := testDevices()
	hot, cool := devs[0], devs[1]
	for _, tc := range []struct {
		query string
		d     device.Device
		p     device.Process
		want  bool
	}{
		{"pid>1000", hot, hot.Procs[0], true},
		{"pid<1000", hot, hot.Procs[0], false},
		{"pid<1000", cool, cool.Procs[0], true},
		{"mem>40", hot, hot.Procs[0], true}, // 40G of the device's 80G
		{"mem>40", cool, cool.Procs[0], false},
		{"mem_used>=40G", hot, hot.Procs[0], true},
		{"util>80", hot, hot.Procs[0], true},  // the process reports its own util
		{"temp>=70", hot, hot.Procs[0], true}, // falls back to the device
		{"temp>=70", cool, cool.Procs[0], false},
		{"util>50", cool, cool.Procs[0], false}, // no process util, device is at 12
		{"!ns:ml", hot, hot.Procs[0], false},
		{"!ns:ml", cool, cool.Procs[0], true},
	} {
		if got := ParseFilter(tc.query).Process(tc.d, tc.p); got != tc.want {
			t.Errorf("%q on pid %d = %v, want %v", tc.query, tc.p.PID, got, tc.want)
		}
	}
}

func TestFilterEvents(t *testing.T) {
	e := events.Event{Severity: events.Warning, Kind: "thermal", Label: "0", Message: "device 0 is hot"}
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"sev:warning", true},
		{"severity:critical", false},
		{"!sev:critical", true},
		{"!sev:warning", false},
		{"kind:thermal dev:0", true},
		{"!hot", false},
		{"temp>70", false}, // events carry no numbers
	} {
		if got := ParseFilter(tc.query).Event(e); got != tc.want {
			t.Errorf("%q on event = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestFilterEmpty(t *testing.T) {
	for q, want := range map[string]bool{
		"":            true,
		"   ":         true,
		"python":      false,
		"ns:ml":       false,
		"!ns:ml":      false,
		"!python":     false,
		"util>80":     false,
		"!util>80":    false,
		"util>banana": false, // parses as a word, not a comparison
	} {
		if got := ParseFilter(q).Empty(); got != want {
			t.Errorf("Empty(%q) = %v, want %v", q, got, want)
		}
	}
}
