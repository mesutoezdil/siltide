// Package smi covers vendors whose cgo-free interface is a management CLI
// (`npu-smi`, `cnmon`, `efsmi`, ...) or, for AMD and Intel, the Linux DRM
// (Direct Rendering Manager) sysfs tree. Each vendor is a Spec: where the
// tool lives and how to read its output into devices. Values a tool does
// not print are left out, never 0.
package smi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

// Spec describes one vendor.
type Spec struct {
	Vendor device.Vendor
	Label  string
	// Tools are candidate executables: bare names looked up on PATH, or
	// absolute paths, with * globs allowed.
	Tools []string
	// Probe replaces the tool lookup (sysfs vendors). Optional.
	Probe func() error
	// Args and Parse describe a plain "run the tool, parse stdout" vendor;
	// they also let the SSH provider run the same tool on another host.
	Args  []string
	Env   []string // KEY=value pairs the tool needs
	Parse func(out []byte) ([]device.Device, error)
	// Read replaces Args/Parse for vendors that need more than one call.
	Read func(ctx context.Context, path string) ([]device.Device, error)
	// Hint tells the user what to install when the tool is missing.
	Hint string
	// Close stops background work (`neuron-monitor`). Optional.
	Close func()
}

// Provider wraps the spec.
func (s Spec) Provider() provider.Provider {
	var path string
	return provider.Provider{
		Name: string(s.Vendor), Label: s.Label, Hint: s.Hint, Close: s.Close,
		Detect: func() error {
			if s.Probe != nil {
				return s.Probe()
			}
			var err error
			path, err = locate(s.Tools)
			return err
		},
		Read: func(ctx context.Context) ([]device.Device, error) { return s.read(ctx, path) },
	}
}

func (s Spec) read(ctx context.Context, path string) ([]device.Device, error) {
	devs, err := s.readRaw(ctx, path)
	src := s.source(path)
	for i := range devs {
		if devs[i].Source == "" {
			devs[i].Source = src
		}
	}
	return devs, err
}

func (s Spec) readRaw(ctx context.Context, path string) ([]device.Device, error) {
	if s.Read != nil {
		return s.Read(ctx, path)
	}
	out, err := runEnv(ctx, s.Env, path, s.Args...)
	if err != nil {
		return nil, err
	}
	return s.Parse(out)
}

// source names where the numbers come from: the tool, or sysfs.
func (s Spec) source(path string) string {
	if s.Probe != nil {
		return "sysfs"
	}
	return filepath.Base(path)
}

// Specs lists every CLI or sysfs vendor.
func Specs() []Spec {
	return []Spec{
		AMD(), Intel(), Ascend(), Neuron(), Biren(), Cambricon(), Enflame(),
		Hygon(), Iluvatar(), Kunlunxin(), MetaX(), MThreads(), VastAI(),
	}
}

// Runner executes a shell command line somewhere else (SSH) and returns
// stdout.
type Runner func(ctx context.Context, cmdline string) ([]byte, error)

// Remote returns providers that run the CLI vendors through run, for hosts
// where siltide itself is not installed. Vendors that need a library, sysfs
// or a long-running monitor are left out.
func Remote(node string, run Runner) []provider.Provider {
	var out []provider.Provider
	for _, s := range Specs() {
		if s.Parse == nil || len(s.Tools) == 0 {
			continue
		}
		var path string
		out = append(out, provider.Provider{
			Name: "node:" + node + ":" + string(s.Vendor), Label: s.Label + " on " + node, Hint: s.Hint,
			Detect: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				var probes []string
				for _, t := range s.Tools {
					probes = append(probes, "command -v "+shellQuote(t))
				}
				b, err := run(ctx, strings.Join(probes, " || "))
				if err != nil || strings.TrimSpace(string(b)) == "" {
					return fmt.Errorf("%s not found on %s", s.Tools[0], node)
				}
				path = strings.TrimSpace(strings.Split(string(b), "\n")[0])
				return nil
			},
			Read: func(ctx context.Context) ([]device.Device, error) {
				parts := append([]string{}, s.Env...)
				parts = append(parts, shellQuote(path))
				for _, a := range s.Args {
					parts = append(parts, shellQuote(a))
				}
				b, err := run(ctx, strings.Join(parts, " "))
				if err != nil {
					return nil, err
				}
				devs, err := s.Parse(b)
				for i := range devs {
					devs[i].Node = node
					devs[i].ID = node + "/" + devs[i].ID
					devs[i].Source = filepath.Base(path) + " over ssh"
				}
				return devs, err
			},
		})
	}
	return out
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,*", r)
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func locate(tools []string) (string, error) {
	for _, t := range tools {
		cands := []string{t}
		if strings.Contains(t, "*") {
			cands, _ = filepath.Glob(t)
		}
		for _, c := range cands {
			if p, err := exec.LookPath(c); err == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s not found", strings.Join(tools, " / "))
}

// Vendors returns a provider for every CLI or sysfs backed vendor.
func Vendors() []provider.Provider {
	specs := Specs()
	out := make([]provider.Provider, len(specs))
	for i, s := range specs {
		out[i] = s.Provider()
	}
	return out
}

// run executes a tool and returns stdout.
func run(ctx context.Context, path string, args ...string) ([]byte, error) {
	return runEnv(ctx, nil, path, args...)
}

// runEnv is run with extra environment variables.
func runEnv(ctx context.Context, env []string, path string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %w: %s", filepath.Base(path), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return out, nil
}
