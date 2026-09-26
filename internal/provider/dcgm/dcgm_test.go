package dcgm

import (
	"testing"

	"github.com/moezdil/siltide/internal/device"
)

func TestParse(t *testing.T) {
	out := "# Entity  SMACT  SMOCC  TENSO  DRAMA\n# ID\nGPU 0     0.912  0.455  0.610  0.380\nGPU 1     N/A    N/A    N/A    N/A\n"
	got := Parse(out)
	if got[0][device.SMActive] != 91.2 || got[0][device.TensorActive] != 61 || len(got[1]) != 0 {
		t.Fatalf("%+v", got)
	}
}
