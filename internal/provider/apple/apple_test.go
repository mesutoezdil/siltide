package apple

import (
	"testing"

	"github.com/moezdil/siltide/internal/device"
)

func TestParse(t *testing.T) {
	out := `+-o AGXAcceleratorG16X  <class AGXAcceleratorG16X>
    {
      "gpu-core-count" = 20
      "PerformanceStatistics" = {"Device Utilization %"=33,"In use system memory"=1133346816}
      "model" = "Apple M4 Pro"
    }
`
	g := Parse(out, 48<<30)
	if len(g) != 1 || g[0].Name != "Apple M4 Pro 20-core GPU" || g[0].Metrics[device.Util] != 33 ||
		g[0].Metrics[device.MemUsed] != 1133346816 || g[0].Metrics[device.MemTotal] != 48<<30 {
		t.Fatalf("got %+v", g)
	}
	if _, ok := g[0].Metrics.Get(device.Temp); ok {
		t.Error("temperature must be absent")
	}
}
