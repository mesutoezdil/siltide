package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/kube"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/tui"
)

// flagNames are every long flag, for completions.
var flagNames = []string{
	"config", "interval", "demo", "demo-devices", "once", "json", "vendors", "listen", "service", "remote", "token",
	"gen-token", "no-history", "retention", "theme", "list-themes", "print-config", "diagnose", "diagnose-offline",
	"tab", "filter", "node", "namespace", "bookmark", "mcp-stdio", "mcp-http",
	"debug", "log-file", "version", "update", "record", "replay", "status", "export", "completion", "man",
}

// completion prints a shell completion script.
func completion(shell string) (string, error) {
	flags := "--" + strings.Join(flagNames, " --")
	switch shell {
	case "bash":
		return fmt.Sprintf(`# siltide bash completion: source this file or drop it in /etc/bash_completion.d
_siltide() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local prev="${COMP_WORDS[COMP_CWORD-1]}"
    case "$prev" in
        --theme) COMPREPLY=($(compgen -W "%s" -- "$cur")); return ;;
        --vendors) COMPREPLY=($(compgen -W "%s" -- "$cur")); return ;;
        --completion) COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur")); return ;;
        --config|--log-file|--record|--replay|--export) COMPREPLY=($(compgen -f -- "$cur")); return ;;
    esac
    COMPREPLY=($(compgen -W "%s" -- "$cur"))
}
complete -F _siltide siltide
`, strings.Join(tui.ThemeNames(), " "), strings.Join(names(), " "), flags), nil
	case "zsh":
		var lines []string
		for _, f := range flagNames {
			lines = append(lines, fmt.Sprintf("  '--%s'", f))
		}
		return fmt.Sprintf(`#compdef siltide
# siltide zsh completion: put this file in a directory on $fpath as _siltide
_arguments \
%s
`, strings.Join(lines, " \\\n")), nil
	case "fish":
		var b strings.Builder
		b.WriteString("# siltide fish completion: ~/.config/fish/completions/siltide.fish\n")
		for _, f := range flagNames {
			fmt.Fprintf(&b, "complete -c siltide -l %s\n", f)
		}
		fmt.Fprintf(&b, "complete -c siltide -l theme -xa '%s'\n", strings.Join(tui.ThemeNames(), " "))
		fmt.Fprintf(&b, "complete -c siltide -l vendors -xa '%s'\n", strings.Join(names(), " "))
		b.WriteString("complete -c siltide -l completion -xa 'bash zsh fish'\n")
		return b.String(), nil
	}
	return "", fmt.Errorf("unknown shell %q (bash, zsh, fish)", shell)
}

// manPage renders a roff manual page.
func manPage() string {
	var flags strings.Builder
	for _, f := range flagNames {
		fmt.Fprintf(&flags, ".TP\n.B \\-\\-%s\n", f)
		switch f {
		case "demo":
			flags.WriteString("Show a simulated mixed fleet instead of real hardware.\n")
		case "once":
			flags.WriteString("Print one snapshot and exit; exit code 3 when nothing was found.\n")
		case "json":
			flags.WriteString("JSON output: one snapshot with --once, otherwise a stream.\n")
		case "service":
			flags.WriteString("Run headless: collect, keep history, and serve the API and /metrics.\n")
		case "remote":
			flags.WriteString("Watch a remote siltide service (URL) instead of local hardware.\n")
		case "record":
			flags.WriteString("Append every snapshot as JSON to this file; replay it with --replay.\n")
		case "replay":
			flags.WriteString("Drive the interface from a recording instead of hardware.\n")
		case "status":
			flags.WriteString("Print a one-line summary for tmux, i3bar, or a prompt.\n")
		case "export":
			flags.WriteString("Write the on-disk history as CSV to this file and exit.\n")
		case "completion":
			flags.WriteString("Print a completion script for bash, zsh, or fish.\n")
		case "mcp-stdio":
			flags.WriteString("Answer Model Context Protocol calls on stdin and stdout. Read only.\n")
		case "mcp-http":
			flags.WriteString("Answer Model Context Protocol calls on a loopback address, with the configured token.\n")
		case "update":
			flags.WriteString("Replace this binary with the newest release, after checking its published SHA\\-256. Refused for an install a package manager owns.\n")
		case "diagnose":
			flags.WriteString("Print build, config, state, and vendor detection details and exit.\n")
		case "diagnose-offline":
			flags.WriteString("Like \\-\\-diagnose, but probe no hardware.\n")
		default:
			flags.WriteString("See siltide --help.\n")
		}
	}
	return fmt.Sprintf(`.TH SILTIDE 1 "%s" "siltide %s" "User Commands"
.SH NAME
siltide \- terminal monitor for GPUs, NPUs, and other AI accelerators
.SH SYNOPSIS
.B siltide
[\fIflags\fR]
.SH DESCRIPTION
siltide shows what every accelerator in the box (or the fleet) is doing right
now and, with its built-in time machine, what it did earlier. It reads NVIDIA,
AMD, Intel, Apple silicon, Huawei Ascend, AWS Inferentia and Trainium, Biren,
Cambricon, Enflame, Hygon, Iluvatar, Kunlunxin, MetaX, Moore Threads, and VastAI
devices without root, cgo, or a daemon, correlates processes with containers,
pods, Kubernetes workloads, and Slurm jobs, and exports everything as JSON and
Prometheus metrics.
.SH OPTIONS
%s
.SH FILES
.TP
.I ~/.config/siltide/config.yaml
Configuration; see examples/config.yaml. Unknown keys are rejected.
.TP
.I ~/.config/siltide/config.d/*.yaml
Drop-in configuration, read in name order after config.yaml; later files win
key by key. \-\-config may also name a directory of *.yaml files.
.TP
.I ~/.config/siltide/themes/*.yaml
User themes.
.TP
.I ~/.local/state/siltide/history/
History files, one per hour, plain text with a CRC per line.
.SH EXIT STATUS
0 on success, 1 on error, 2 on usage, 3 when --once found no accelerator.
.SH SEE ALSO
nvidia-smi(1), npu-smi(1)
`, time.Now().Format("2006-01-02"), version, flags.String())
}

// statusLine is the one-line summary for status bars.
func statusLine(s collect.Snapshot) string {
	if len(s.Devices) == 0 {
		return "siltide: no accelerators"
	}
	f := s.Fleet
	parts := []string{fmt.Sprintf("%d dev", f.Devices)}
	if !math.IsNaN(f.AvgUtil) {
		parts = append(parts, fmt.Sprintf("%.0f%% util", f.AvgUtil))
	}
	if f.MemTotal > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% mem", f.MemUsed/f.MemTotal*100))
	}
	if f.PowerW > 0 {
		parts = append(parts, fmt.Sprintf("%.0fW", f.PowerW))
	}
	if !math.IsNaN(f.MaxTemp) {
		parts = append(parts, fmt.Sprintf("%.0f°C", f.MaxTemp))
	}
	parts = append(parts, fmt.Sprintf("health %.0f", f.AvgHealth))
	if n := len(s.Alerts); n > 0 {
		parts = append(parts, fmt.Sprintf("▲%d", n))
	}
	worst := ""
	for _, d := range s.Devices {
		if d.State == device.StateDown {
			worst = " DOWN:" + d.Label()
			break
		}
	}
	return strings.Join(parts, " · ") + worst
}

// exportHistory writes the store as CSV to path.
func exportHistory(path string, eng *collect.Engine) error {
	h := eng.History()
	if h == nil {
		return fmt.Errorf("history is disabled")
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var devs []device.Device
	for _, id := range h.IDs() {
		devs = append(devs, device.Device{ID: id})
	}
	n, err := tui.ExportCSV(f, h, devs)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "siltide: wrote %d rows to %s\n", n, path)
	return nil
}

// diagnose reports what siltide sees about its own environment: the build, the
// files it reads and writes, the settings in force, and which vendors answer
// here. detect is false for --diagnose-offline, which probes no hardware.
func diagnose(cfg config.Config, cfgPath string, provs []provider.Provider, detect bool) string {
	var b strings.Builder
	row := func(k, format string, args ...any) {
		fmt.Fprintf(&b, "  %-10s %s\n", k, fmt.Sprintf(format, args...))
	}

	b.WriteString("build\n")
	row("version", "%s", version)
	row("go", "%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	row("revision", "%s", revision())

	b.WriteString("files\n")
	if files, err := config.Files(cfgPath, false); err != nil || len(files) == 0 {
		row("config", "%s (%s)", cfgPath, fileState(cfgPath))
	} else {
		for i, f := range files {
			key := "config"
			if i > 0 {
				key = "" // a drop-in, applied after the line above it
			}
			row(key, "%s", f)
		}
	}
	themes := filepath.Join(config.ConfigDir(), "themes")
	if files, _ := filepath.Glob(filepath.Join(themes, "*.yaml")); len(files) > 0 {
		row("themes", "%s (%d user themes)", themes, len(files))
	} else {
		row("themes", "%s (none)", themes)
	}
	row("state", "%s (%s)", config.StateDir(), dirState(config.StateDir()))
	switch {
	case !cfg.History.Persist:
		row("history", "off (history.persist is false)")
	default:
		dir := cfg.History.Dir
		if dir == "" {
			dir = filepath.Join(config.StateDir(), "history")
		}
		row("history", "%s (%s)", dir, dirUsage(dir))
	}
	if cfg.Log != "" {
		row("log", "%s", cfg.Log)
	} else {
		row("log", "off (--debug writes %s)", filepath.Join(config.StateDir(), "siltide.log"))
	}

	b.WriteString("settings\n")
	row("refresh", "%s", cfg.Refresh)
	if len(cfg.Vendors) > 0 {
		row("vendors", "%s", strings.Join(cfg.Vendors, ", "))
	} else {
		row("vendors", "all")
	}
	if cfg.Listen != "" {
		row("listen", "%s", cfg.Listen)
	} else {
		row("listen", "off")
	}
	row("nodes", "%d", len(cfg.Nodes))
	row("theme", "%s", cfg.Theme)

	b.WriteString("kubernetes\n")
	pods := kube.New(kube.Options{Kubeconfig: cfg.Kubernetes.Kubeconfig, Context: cfg.Kubernetes.Context})
	if pods.Enabled() {
		row("source", "%s", pods.Source())
	}
	for _, a := range pods.Attempts() {
		state := "ok"
		if a.Err != "" {
			state = a.Err
		}
		fmt.Fprintf(&b, "  %-10s %-44s %s\n", "", cut(a.What+" "+a.Where, 44), state)
	}
	if len(pods.Attempts()) == 0 {
		row("looked", "nowhere: no pod source is configured")
	}

	b.WriteString("providers\n")
	if !detect {
		row("detection", "skipped (--diagnose-offline)")
		return b.String()
	}
	active, total := 0, time.Duration(0)
	for _, p := range provs {
		start := time.Now()
		err := p.Detect()
		took := time.Since(start)
		total += took
		state, detail := "ok", p.Label
		if err != nil {
			state, detail = "absent", err.Error()
		} else {
			active++
		}
		fmt.Fprintf(&b, "  %-10s %8s  %-7s %s\n", p.Name, ms(took), state, detail)
		if err != nil && p.Hint != "" {
			fmt.Fprintf(&b, "  %-10s %8s  %-7s %s\n", "", "", "", p.Hint)
		}
	}
	row("detection", "%d probed, %d available, %s total", len(provs), active, ms(total))
	return b.String()
}

// cut shortens s to w characters, ending in an ellipsis when it had to.
func cut(s string, w int) string {
	if len(s) <= w {
		return s
	}
	return s[:w-1] + "…"
}

// ms renders a detection time the way a person reads it.
func ms(d time.Duration) string { return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond)) }

// revision reads the commit the binary was built from, "unknown" outside a
// module build.
func revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	var rev, when string
	dirty := ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = " (dirty)"
			}
		}
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return strings.TrimSpace(rev+" "+when) + dirty
}

// fileState says whether a config file is there to read.
func fileState(path string) string {
	switch fi, err := os.Stat(path); {
	case err == nil && fi.IsDir():
		return "is a directory"
	case err == nil:
		return "loaded"
	case os.IsNotExist(err):
		return "missing, defaults in use"
	default:
		return err.Error()
	}
}

// dirState says whether a directory exists and takes writes.
func dirState(dir string) string {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return "missing, created on first run"
		}
		return err.Error()
	}
	f, err := os.CreateTemp(dir, ".siltide-diagnose-*")
	if err != nil {
		return "not writable: " + err.Error()
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return "writable"
}

// dirUsage counts what history has on disk.
func dirUsage(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "empty, created on first run"
		}
		return err.Error()
	}
	var size int64
	for _, e := range entries {
		if fi, err := e.Info(); err == nil {
			size += fi.Size()
		}
	}
	return fmt.Sprintf("%d files, %.1f MB", len(entries), float64(size)/(1<<20))
}
