// siltide monitors GPUs, NPUs, and other AI accelerators from the terminal.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/history"
	"github.com/moezdil/siltide/internal/mcp"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/provider/apple"
	"github.com/moezdil/siltide/internal/provider/dcgm"
	"github.com/moezdil/siltide/internal/provider/nvidia"
	"github.com/moezdil/siltide/internal/provider/remote"
	"github.com/moezdil/siltide/internal/provider/replay"
	"github.com/moezdil/siltide/internal/provider/sim"
	"github.com/moezdil/siltide/internal/provider/smi"
	"github.com/moezdil/siltide/internal/provider/sshp"
	"github.com/moezdil/siltide/internal/server"
	"github.com/moezdil/siltide/internal/tui"
	"github.com/moezdil/siltide/internal/update"
)

// version is set by the release build.
var version = "dev"

// exit codes
const (
	exitError    = 1
	exitUsage    = 2
	exitNoDevice = 3 // --once found no accelerator
)

func main() {
	fs := flag.NewFlagSet("siltide", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "config file or directory of *.yaml files (default "+config.Path()+")")
	interval := fs.Duration("interval", 0, "refresh interval (overrides refresh in the config)")
	demo := fs.Bool("demo", false, "show a simulated mixed fleet instead of real hardware")
	demoN := fs.Int("demo-devices", 8, "NVIDIA-class devices in the simulated fleet")
	once := fs.Bool("once", false, "print one snapshot and exit (exit code 3 when nothing was found)")
	asJSON := fs.Bool("json", false, "JSON output: one snapshot with --once, otherwise a stream of snapshots")
	only := fs.String("vendors", "", "comma-separated vendors to probe (default: config, then all)")
	listen := fs.String("listen", "", "serve /api and /metrics on this address, e.g. 127.0.0.1:9800 (overrides the config)")
	service := fs.Bool("service", false, "run headless: collect, keep history, and serve; needs --listen or listen in the config")
	remoteURL := fs.String("remote", "", "watch a remote siltide service instead of local hardware, e.g. https://node:9800")
	token := fs.String("token", "", "bearer token for --remote or --listen (overrides the config)")
	genToken := fs.Bool("gen-token", false, "print a fresh API token and its SHA-256 digest and exit")
	noHistory := fs.Bool("no-history", false, "keep no history on disk")
	retention := fs.Duration("retention", 0, "how much history to keep (overrides history.keep)")
	theme := fs.String("theme", "", "theme name (overrides the config)")
	listThemes := fs.Bool("list-themes", false, "list built-in and user themes and exit")
	printConfig := fs.Bool("print-config", false, "print the effective configuration and exit")
	diag := fs.Bool("diagnose", false, "print build, config, state, and vendor detection details and exit")
	diagOffline := fs.Bool("diagnose-offline", false, "like --diagnose, but probe no hardware")
	debug := fs.Bool("debug", false, "log collector activity to --log-file (default state dir/siltide.log)")
	logFile := fs.String("log-file", "", "debug log file")
	showVersion := fs.Bool("version", false, "print the version and exit")
	doUpdate := fs.Bool("update", false, "replace this binary with the newest release, after verifying its checksum")
	recordPath := fs.String("record", "", "append every snapshot as JSON to this file (replay with --replay)")
	replayPath := fs.String("replay", "", "drive the interface from a recording instead of hardware")
	openTab := fs.String("tab", "", "open on this tab (overview, devices, processes, history, ...)")
	openFilter := fs.String("filter", "", "open with this filter, as / sets it")
	openNode := fs.String("node", "", "open limited to this node")
	openNS := fs.String("namespace", "", "open the Kubernetes tab limited to this namespace")
	openBookmark := fs.String("bookmark", "", "open a view saved with :bookmark save")
	status := fs.Bool("status", false, "print a one-line summary for tmux, i3bar, or a prompt and exit")
	exportPath := fs.String("export", "", "write the on-disk history as CSV to this file and exit")
	completionShell := fs.String("completion", "", "print a completion script for bash, zsh, or fish and exit")
	mcpStdio := fs.Bool("mcp-stdio", false, "serve the Model Context Protocol on stdin and stdout, for an agent that spawns siltide")
	mcpHTTP := fs.String("mcp-http", "", "serve the Model Context Protocol on this loopback address, e.g. 127.0.0.1:8765")
	man := fs.Bool("man", false, "print the manual page (roff) and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), `siltide %s: GPU, NPU, and AI accelerator monitor for the terminal

Usage:
  siltide                      interactive terminal UI (TUI), vendors auto-detected
  siltide --demo               explore every view with a simulated fleet
  siltide --once [--json]      one snapshot on stdout
  siltide --json               stream one JSON snapshot per refresh
  siltide --listen :9800       TUI plus API and Prometheus /metrics
  siltide --service            headless collector for fleets and Prometheus
  siltide --remote URL         TUI attached to another siltide's --listen
  siltide --record f.jsonl     record while running; siltide --replay f.jsonl plays it back
  siltide --status             one line for tmux, i3bar, or a prompt
  siltide --mcp-stdio          answer an agent over the Model Context Protocol
  siltide --update             move to the newest release, checksum verified
  siltide --diagnose           build, config, state, and vendor detection report

Vendors: %s

Flags:
`, version, strings.Join(names(), ", "))
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(exitUsage)
	}
	switch {
	case *showVersion:
		fmt.Println("siltide", version)
		return
	case *completionShell != "":
		script, err := completion(*completionShell)
		if err != nil {
			fail(err)
		}
		fmt.Print(script)
		return
	case *man:
		fmt.Print(manPage())
		return
	case *doUpdate:
		self, err := update.Self()
		if err != nil {
			fail(err)
		}
		if err := update.Run(update.HTTP(), self, version, update.ChannelOf(version), os.Stdout); err != nil {
			fail(err)
		}
		return
	case *genToken:
		b := make([]byte, 24)
		_, _ = rand.Read(b)
		tok := hex.EncodeToString(b)
		fmt.Printf("token:  %s\nsha256: %s\n\nPut \"sha256:%s\" under token: in the service's config and the plain token in the client's.\n", tok, server.Digest(tok), server.Digest(tok))
		return
	case *listThemes:
		fmt.Println("built in:", strings.Join(tui.ThemeNames(), ", "))
		if files, _ := filepath.Glob(filepath.Join(config.ConfigDir(), "themes", "*.yaml")); len(files) > 0 {
			var names []string
			for _, f := range files {
				names = append(names, strings.TrimSuffix(filepath.Base(f), ".yaml"))
			}
			fmt.Println("user:    ", strings.Join(names, ", "))
		}
		return
	}

	path, explicit := config.Path(), false
	if *cfgPath != "" {
		path, explicit = *cfgPath, true
	}
	// loadConfig reads the file and lays the flags over it. `:reload` in the
	// interface calls it again, so a flag keeps winning over the file.
	loadConfig := func() (config.Config, error) {
		cfg, err := config.Load(path, explicit)
		if err != nil {
			return cfg, err
		}
		if *interval > 0 {
			cfg.Refresh = *interval
		}
		if *retention > 0 {
			cfg.History.Keep = *retention
		}
		if *listen != "" {
			cfg.Listen = *listen
		}
		if *token != "" {
			cfg.Token = *token
		}
		if *only != "" {
			cfg.Vendors = strings.Split(*only, ",")
		}
		if *theme != "" {
			cfg.Theme = *theme
		}
		if *logFile != "" {
			cfg.Log = *logFile
		}
		return cfg, cfg.Validate()
	}
	cfg, err := loadConfig()
	if err != nil {
		fail(err)
	}
	if *printConfig {
		fmt.Print(cfg.Print())
		return
	}
	if *diag || *diagOffline {
		provs, err := providers(cfg.Vendors)
		if err != nil {
			fail(err)
		}
		fmt.Print(diagnose(cfg, path, provs, *diag))
		return
	}
	if *service && cfg.Listen == "" {
		fail(errors.New("--service needs --listen or listen: in the config"))
	}
	logPath := ""
	if *debug || cfg.Log != "" {
		logPath = cfg.Log
		if logPath == "" {
			logPath = filepath.Join(config.StateDir(), "siltide.log")
		}
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fail(err)
		}
		defer func() { _ = f.Close() }()
		log.SetOutput(f)
		log.Printf("siltide %s starting, config %s", version, path)
	} else {
		log.SetOutput(nullWriter{})
	}

	var provs []provider.Provider
	switch {
	case *replayPath != "":
		provs = []provider.Provider{replay.Provider(*replayPath)}
		*noHistory = true
	case *demo:
		provs = []provider.Provider{sim.Provider(*demoN)}
	case *remoteURL != "":
		provs = []provider.Provider{remote.Provider(remote.Options{Name: hostOf(*remoteURL), URL: *remoteURL, Token: cfg.Token})}
	default:
		provs, err = providers(cfg.Vendors)
		if err != nil {
			fail(err)
		}
		provs = append(provs, dcgm.Provider())
		for _, n := range cfg.Nodes {
			if n.SSH != "" {
				provs = append(provs, sshp.Providers(n.Name, n.SSH, n.Key)...)
				continue
			}
			provs = append(provs, remote.Provider(remote.Options{Name: n.Name, URL: n.URL, Token: n.Token, TokenFile: n.TokenFile, CA: n.CA}))
		}
	}

	var hist *history.Store
	if !*once {
		dir := ""
		if cfg.History.Persist && !*noHistory {
			dir = cfg.History.Dir
			if dir == "" {
				dir = filepath.Join(config.StateDir(), "history")
				if *demo {
					dir = filepath.Join(config.StateDir(), "history-demo") // never mix demo data with real history
				}
			}
		}
		hist, err = history.Open(history.Options{Keep: cfg.History.Keep, Resolution: cfg.History.Resolution, Dir: dir, MaxDisk: int64(cfg.History.MaxDiskMB) << 20})
		if err != nil {
			fail(err)
		}
		if hist.Warning != "" {
			log.Print(hist.Warning)
		}
	}
	if *exportPath != "" {
		if hist == nil {
			fail(errors.New("--export needs history"))
		}
		eng := collect.New(nil, cfg, hist, false)
		if err := exportHistory(*exportPath, eng); err != nil {
			fail(err)
		}
		hist.Close()
		return
	}
	eng := collect.New(provs, cfg, hist, *demo)
	defer eng.Close()
	if *recordPath != "" {
		f, err := os.OpenFile(*recordPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fail(err)
		}
		defer func() { _ = f.Close() }()
		eng.Record(f)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// uiOptions maps a config onto the interface. A theme that does not
	// resolve falls back to the default and says so, at startup on stderr and
	// on a reload by the interface changing colour.
	uiOptions := func(cfg config.Config) (tui.Options, error) {
		th, err := tui.LoadTheme(cfg.Theme, filepath.Join(config.ConfigDir(), "themes"), cfg.Colors, cfg.Transparent)
		mouse := cfg.Mouse == nil || *cfg.Mouse
		return tui.Options{
			Theme: th, Keys: tui.NewKeymap(cfg.Keys), TempWarn: cfg.Thresholds.TempWarn,
			Mouse: mouse, Currency: cfg.Cost.Currency, LogFile: logPath,
			Open:      tui.OpenView(*openBookmark, *openTab, *openFilter, *openNode, *openNS),
			NoSession: *demo || *replayPath != "",
		}, err
	}
	opts, terr := uiOptions(cfg)
	th := opts.Theme
	if terr != nil {
		fmt.Fprintln(os.Stderr, "siltide:", terr, "(using default)")
	}

	if *status {
		eng.Detect()
		snap := eng.Collect(ctx)
		fmt.Println(statusLine(snap))
		if len(snap.Devices) == 0 {
			os.Exit(exitNoDevice)
		}
		return
	}
	if *once {
		eng.Detect()
		snap := eng.Collect(ctx)
		if *asJSON {
			writeJSON(snap)
		} else {
			fmt.Print(tui.Plain(snap, th, 120))
		}
		if len(snap.Devices) == 0 {
			os.Exit(exitNoDevice)
		}
		return
	}

	if cfg.Listen != "" {
		srv, err := server.New(eng, server.Options{Listen: cfg.Listen, Token: cfg.Token, Cert: cfg.TLS.Cert, Key: cfg.TLS.Key, ClientCA: cfg.TLS.ClientCA, Insecure: cfg.Insecure})
		if err != nil {
			fail(err)
		}
		go func() {
			if err := server.Serve(srv); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fail(err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		}()
	}

	go eng.Run(ctx)
	switch {
	case *mcpStdio:
		// Nothing but protocol may reach stdout while this runs.
		log.SetOutput(os.Stderr)
		firstSnapshot(ctx, eng)
		// A server on stdin says nothing until it is spoken to, which from a
		// terminal looks like a program that did not start. stderr is not the
		// protocol stream, so a line here costs the agent nothing.
		fmt.Fprintf(os.Stderr,
			"siltide %s: Model Context Protocol on stdin and stdout, %d tools, read only.\n"+
				"Waiting for a client. This is meant to be spawned by an agent, not run by hand;\n"+
				"try --mcp-http 127.0.0.1:8765 to poke at it, or ctrl-c to stop.\n",
			version, len(mcp.Tools()))
		// The hint is for a person at a keyboard, so it is offered only when
		// stdin is one. An agent's pipe gets the protocol and nothing else.
		var hint io.Writer
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			hint = os.Stderr
		}
		if err := mcp.New(eng, version).ServeStdio(ctx, os.Stdin, os.Stdout, hint); err != nil && ctx.Err() == nil {
			fail(err)
		}
	case *mcpHTTP != "":
		fmt.Fprintf(os.Stderr, "siltide %s: Model Context Protocol on http://%s\n", version, *mcpHTTP)
		firstSnapshot(ctx, eng)
		if err := mcp.New(eng, version).Serve(ctx, *mcpHTTP, cfg.Token); err != nil && ctx.Err() == nil {
			fail(err)
		}
	case *service:
		fmt.Fprintf(os.Stderr, "siltide %s serving on %s\n", version, cfg.Listen)
		<-ctx.Done()
	case *asJSON:
		ch := eng.Changed()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				writeJSON(eng.Snapshot())
			}
		}
	default:
		var reload func() (tui.Options, error)
		reload = func() (tui.Options, error) {
			cfg, err := loadConfig()
			if err != nil {
				return tui.Options{}, err // nothing changed
			}
			eng.Reconfigure(cfg)
			o, _ := uiOptions(cfg)
			o.Reload = reload
			return o, nil
		}
		opts.Reload = reload
		if err := tui.Run(ctx, eng, opts); err != nil && ctx.Err() == nil {
			fail(err)
		}
	}
}

// firstSnapshot waits for the collector to have something to answer with, so
// the first call an agent makes is not told the fleet is empty. Detection on
// a machine with many vendors is most of that wait.
func firstSnapshot(ctx context.Context, eng *collect.Engine) {
	select {
	case <-eng.Changed():
	case <-ctx.Done():
	case <-time.After(15 * time.Second):
	}
}

type nullWriter struct{}

func (nullWriter) Write(p []byte) (int, error) { return len(p), nil }

func writeJSON(v any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fail(err)
	}
}

func hostOf(url string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexAny(u, ":/"); i > 0 {
		u = u[:i]
	}
	return u
}

func names() []string {
	var out []string
	for _, p := range all() {
		out = append(out, p.Name)
	}
	return out
}

func all() []provider.Provider {
	return append([]provider.Provider{nvidia.Provider(), apple.Provider()}, smi.Vendors()...)
}

func providers(only []string) ([]provider.Provider, error) {
	if len(only) == 0 {
		return all(), nil
	}
	want := map[string]bool{}
	for _, n := range only {
		want[strings.TrimSpace(n)] = true
	}
	var out []provider.Provider
	for _, p := range all() {
		if want[p.Name] {
			out = append(out, p)
			delete(want, p.Name)
		}
	}
	for n := range want {
		return nil, fmt.Errorf("unknown vendor %q (available: %s)", n, strings.Join(names(), ", "))
	}
	return out, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "siltide:", err)
	os.Exit(exitError)
}
