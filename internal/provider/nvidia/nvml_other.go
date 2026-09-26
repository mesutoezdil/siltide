//go:build !linux

package nvidia

import (
	"context"
	"errors"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

// ponytail: NVML on Linux only; add nvml.dll when a Windows user asks.
func Provider() provider.Provider {
	return provider.Provider{
		Name: "nvidia", Label: "NVIDIA (NVML)", Hint: "NVIDIA GPUs are read on Linux.",
		Detect: func() error { return errors.New("NVML is only read on Linux") },
		Read:   func(context.Context) ([]device.Device, error) { return nil, nil },
	}
}
