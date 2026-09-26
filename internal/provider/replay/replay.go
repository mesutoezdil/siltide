// Package replay drives siltide from a recording made with `--record`: the
// JSON stream of snapshots becomes a provider that hands out the recorded
// devices at their original cadence. Share the file and the other side
// sees what you saw.
package replay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

// frame is the part of a snapshot a replay needs.
type frame struct {
	Time    time.Time       `json:"time"`
	Host    string          `json:"host"`
	Devices []device.Device `json:"devices"`
}

// Provider replays path. Reads return the frame whose recorded offset
// matches the time since the replay started; the last frame repeats.
func Provider(path string) provider.Provider {
	var (
		mu     sync.Mutex
		frames []frame
		start  time.Time
	)
	return provider.Provider{
		Name: "replay", Label: "recording " + path,
		Detect: func() error {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			fs, err := Load(f)
			if err != nil {
				return err
			}
			mu.Lock()
			frames, start = fs, time.Now()
			mu.Unlock()
			return nil
		},
		Read: func(context.Context) ([]device.Device, error) {
			mu.Lock()
			defer mu.Unlock()
			if len(frames) == 0 {
				return nil, nil
			}
			offset := time.Since(start)
			cur := frames[0]
			for _, fr := range frames {
				if fr.Time.Sub(frames[0].Time) > offset {
					break
				}
				cur = fr
			}
			out := make([]device.Device, len(cur.Devices))
			for i, d := range cur.Devices {
				d.Node = cur.Host
				out[i] = d
			}
			return out, nil
		},
	}
}

// Load reads a JSON stream of snapshots.
func Load(r io.Reader) ([]frame, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 16<<20), 64<<20)
	var out []frame
	for sc.Scan() {
		var f frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			return nil, fmt.Errorf("recording: %w", err)
		}
		if len(f.Devices) > 0 {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("recording has no snapshots with devices")
	}
	return out, sc.Err()
}
