package collect

import (
	"context"
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/history"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/provider/sim"
)

// BenchmarkCollect measures one full pass (read, enrich, derive, health,
// events, history) on the simulated fleet; the vendor call itself is free,
// so this is siltide's own overhead per device count.
func BenchmarkCollect(b *testing.B) {
	for _, n := range []int{1, 8, 64} {
		b.Run(itoa(n), func(b *testing.B) {
			hist, _ := history.Open(history.Options{Keep: time.Hour, Resolution: time.Nanosecond})
			e := New([]provider.Provider{sim.Provider(n)}, config.Default(), hist, true)
			e.Detect()
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e.Collect(ctx)
			}
		})
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
