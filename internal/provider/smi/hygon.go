package smi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/device"
)

// Hygon reads DCUs from `hy-smi --json`, a `rocm-smi` fork.
func Hygon() Spec {
	return Spec{
		Vendor: device.Hygon, Label: "Hygon DCU (hy-smi)",
		Tools: []string{"hy-smi", "/opt/hyhal/bin/hy-smi"},
		Hint:  "Hygon DCUs need the hyhal driver, which installs /opt/hyhal/bin/hy-smi.",
		Args:  []string{"--showid", "--showproductname", "--showuniqueid", "--showmeminfo", "vram", "--showuse", "--showtemp", "--json"},
		Parse: parseHySmi,
		Read: func(ctx context.Context, path string) ([]device.Device, error) {
			args := []string{"--showid", "--showproductname", "--showuniqueid", "--showmeminfo", "vram", "--showuse", "--showtemp", "--json"}
			out, err := run(ctx, path, append([]string{"--showpower"}, args...)...)
			if err != nil {
				// Not every `hy-smi` release accepts `--showpower`.
				out, err = run(ctx, path, args...)
			}
			if err != nil {
				return nil, err
			}
			return parseHySmi(out)
		},
	}
}

var (
	hyCard    = regexp.MustCompile(`^card(\d+)$`)
	hyUse     = regexp.MustCompile(`^[DH]CU use \(%\)$`)
	hyMemUsed = regexp.MustCompile(`^vram Total Used Memory \((\w+)\)$`)
	hyMemAll  = regexp.MustCompile(`^vram Total Memory \((\w+)\)$`)
)

var hyTemps = []string{
	"Temperature (Sensor edge) (C)", "Temperature (Sensor core) (C)", "Temperature (Sensor junction) (C)",
}

// parseHySmi maps {"card0": {"DCU use (%)": "0", ...}}. The memory unit is in
// the key (MiB in the captured DTK 24.04.3 output, DTK being Hygon's DCU
// Toolkit), so the parser takes the unit from the key instead of assuming one.
func parseHySmi(data []byte) ([]device.Device, error) {
	var cards map[string]map[string]any
	if err := json.Unmarshal(data, &cards); err != nil {
		return nil, fmt.Errorf("hy-smi json: %w", err)
	}
	var out []device.Device
	for key, kv := range cards {
		m := hyCard.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		b := block{}
		for k, v := range kv {
			b[strings.TrimSpace(k)] = fmt.Sprint(v)
		}
		name := b.first("Card Series", "Card series", "Card Model", "Card model")
		switch {
		case name == "":
			name = "Hygon DCU"
		case !strings.Contains(strings.ToLower(name), "hygon"):
			name = "Hygon " + name
		}
		d := device.New(device.Hygon, idx, name, b.first("Unique ID", "Serial Number"), "")
		set(d.Metrics, device.Temp, b.first(hyTemps...), 1)
		set(d.Metrics, device.Power, b.first("Average Graphics Package Power (W)"), 1)
		for k, v := range b {
			switch {
			case hyUse.MatchString(k):
				set(d.Metrics, device.Util, v, 1)
			case hyMemUsed.MatchString(k):
				setBytes(d.Metrics, device.MemUsed, v, strings.ToLower(hyMemUsed.FindStringSubmatch(k)[1]))
			case hyMemAll.MatchString(k):
				setBytes(d.Metrics, device.MemTotal, v, strings.ToLower(hyMemAll.FindStringSubmatch(k)[1]))
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}
