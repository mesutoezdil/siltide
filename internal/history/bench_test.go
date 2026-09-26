package history

import (
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/device"
)

func BenchmarkRecord(b *testing.B) {
	s, _ := Open(Options{Keep: time.Hour, Resolution: time.Nanosecond, Dir: b.TempDir()})
	defer s.Close()
	devs := make([]device.Device, 16)
	for i := range devs {
		devs[i] = dev(float64(i))
	}
	t := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t = t.Add(time.Second)
		s.Record(t, devs)
	}
}
