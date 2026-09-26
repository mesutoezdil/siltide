// Package server exposes the collector over HTTP: a JSON API for remote
// siltide instances and a Prometheus scrape endpoint. No dependencies: the
// exposition format is a few lines of text.
//
// Security: the token is compared as a SHA-256 digest in constant time, so
// the config may hold `sha256:<digest>` instead of the plaintext. Binding
// outside loopback needs TLS and a token unless `insecure` is set. Optional
// mTLS through a client CA. Process command lines are never served.
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/device"
)

// Schema is the JSON snapshot schema version served by the API.
const Schema = collect.Schema

// Options are the server settings.
type Options struct {
	Listen   string
	Token    string // plaintext or "sha256:<hex>"
	Cert     string
	Key      string
	ClientCA string
	Insecure bool
}

// Digest returns the SHA-256 hex digest of a token.
func Digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// New builds the HTTP server. It refuses unsafe settings.
func New(e *collect.Engine, o Options) (*http.Server, error) {
	if !config.Loopback(o.Listen) && !o.Insecure && (o.Cert == "" || o.Token == "") {
		return nil, errors.New("listening outside loopback needs tls.cert/tls.key and a token (or insecure: true)")
	}
	srv := &http.Server{Addr: o.Listen, Handler: Handler(e, o.Token), ReadHeaderTimeout: 5 * time.Second}
	if o.Cert != "" {
		cert, err := tls.LoadX509KeyPair(o.Cert, o.Key)
		if err != nil {
			return nil, fmt.Errorf("tls: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if o.ClientCA != "" {
			pem, err := os.ReadFile(o.ClientCA)
			if err != nil {
				return nil, fmt.Errorf("tls.client_ca: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("tls.client_ca: no certificates found")
			}
			srv.TLSConfig.ClientCAs = pool
			srv.TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert
		}
	}
	return srv, nil
}

// Serve runs the server until it is shut down.
func Serve(srv *http.Server) error {
	if srv.TLSConfig != nil {
		return srv.ListenAndServeTLS("", "")
	}
	return srv.ListenAndServe()
}

// Handler returns the HTTP handler. An empty token disables authentication.
func Handler(e *collect.Engine, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, public(e.Snapshot())) })
	mux.HandleFunc("GET /api/summary", func(w http.ResponseWriter, _ *http.Request) {
		s := e.Snapshot()
		writeJSON(w, map[string]any{"schema": Schema, "time": s.Time, "host": s.Host, "fleet": s.Fleet, "alerts": len(s.Alerts), "devices": len(s.Devices)})
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, e.Events()) })
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		h := e.History()
		if h == nil {
			http.Error(w, "history disabled", http.StatusNotFound)
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 {
			n = 600
		}
		id := r.URL.Query().Get("id")
		out := map[string]any{}
		for _, k := range []device.Metric{device.Util, device.MemUsed, device.MemTotal, device.Temp, device.Power, device.ClockCore} {
			out[string(k)] = h.Recent(id, k, n)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, Prometheus(e.Snapshot()))
	})
	if token == "" {
		return mux
	}
	want := strings.TrimPrefix(token, "sha256:")
	if !strings.HasPrefix(token, "sha256:") {
		want = Digest(token)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			mux.ServeHTTP(w, r)
			return
		}
		got := Digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// public strips command lines: they can carry secrets.
func public(s collect.Snapshot) collect.Snapshot {
	devs := make([]device.Device, len(s.Devices))
	for i, d := range s.Devices {
		d.Procs = append([]device.Process(nil), d.Procs...)
		for j := range d.Procs {
			d.Procs[j].Command = ""
		}
		devs[i] = d
	}
	s.Devices = devs
	s.Schema = Schema
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// gauges are emitted per device; absent metrics are simply not written.
var gauges = []struct {
	k    device.Metric
	name string
	help string
}{
	{device.Util, "siltide_device_util_percent", "Device utilization in percent."},
	{device.MemUsed, "siltide_device_memory_used_bytes", "Device memory in use."},
	{device.MemTotal, "siltide_device_memory_total_bytes", "Device memory installed."},
	{device.MemBandwidth, "siltide_device_memory_bandwidth_percent", "Memory bandwidth utilization."},
	{device.Temp, "siltide_device_temperature_celsius", "Device temperature."},
	{device.MemTemp, "siltide_device_memory_temperature_celsius", "Memory temperature."},
	{device.Power, "siltide_device_power_watts", "Power draw."},
	{device.PowerCap, "siltide_device_power_cap_watts", "Enforced power limit."},
	{device.Energy, "siltide_device_energy_joules_total", "Energy since driver load."},
	{device.ClockCore, "siltide_device_clock_core_mhz", "Core clock."},
	{device.ClockMem, "siltide_device_clock_memory_mhz", "Memory clock."},
	{device.Fan, "siltide_device_fan_percent", "Fan speed."},
	{device.PState, "siltide_device_pstate", "Performance state, 0 is fastest."},
	{device.Throttle, "siltide_device_throttle_bits", "Active throttle reasons as a bitmask (1 idle, 2 power cap, 4 thermal, 8 hardware, 16 other)."},
	{device.EccCorrected, "siltide_device_ecc_corrected_total", "Corrected ECC (error-correcting code) errors since driver load."},
	{device.EccUncorrected, "siltide_device_ecc_uncorrected_total", "Uncorrected ECC errors since driver load."},
	{device.RemappedRows, "siltide_device_remapped_rows_total", "Rows remapped after memory errors."},
	{device.RemapPending, "siltide_device_remap_pending", "1 when a reset is needed to apply row remaps."},
	{device.RemapFailed, "siltide_device_remap_failed", "1 when a row remap failed."},
	{device.RetiredPages, "siltide_device_retired_pages_total", "Pages retired after memory errors."},
	{device.PCIeGen, "siltide_device_pcie_generation", "Current PCIe link generation."},
	{device.PCIeWidth, "siltide_device_pcie_width", "Current PCIe link width."},
	{device.PCIeReplays, "siltide_device_pcie_replays_total", "PCIe link replays."},
	{device.PCIeRx, "siltide_device_pcie_rx_bytes_per_second", "PCIe traffic into the device."},
	{device.PCIeTx, "siltide_device_pcie_tx_bytes_per_second", "PCIe traffic out of the device."},
	{device.LinksActive, "siltide_device_links_active", "Interconnect links that are up."},
	{device.LinksTotal, "siltide_device_links_total", "Interconnect links present."},
	{device.ViolationPower, "siltide_device_violation_power_percent", "Share of time clocks were held down by power."},
	{device.ViolationTherm, "siltide_device_violation_thermal_percent", "Share of time clocks were held down by heat."},
	{device.Encoder, "siltide_device_encoder_percent", "Video encoder utilization."},
	{device.Decoder, "siltide_device_decoder_percent", "Video decoder utilization."},
	{device.SMActive, "siltide_device_sm_active_percent", "Share of time a streaming multiprocessor (SM) was busy (from DCGM, NVIDIA's Data Center GPU Manager)."},
	{device.SMOccupancy, "siltide_device_sm_occupancy_percent", "Share of warps resident (DCGM profiling)."},
	{device.TensorActive, "siltide_device_tensor_active_percent", "Share of time tensor cores were busy (DCGM profiling)."},
	{device.DRAMActive, "siltide_device_dram_active_percent", "Share of time memory was busy (DCGM profiling)."},
	{device.NUMANode, "siltide_device_numa_node", "NUMA (non-uniform memory access) node of the device."},
	{device.AERCorrected, "siltide_device_pcie_aer_correctable_total", "PCIe AER (Advanced Error Reporting) correctable errors."},
	{device.AERFatal, "siltide_device_pcie_aer_uncorrectable_total", "PCIe AER uncorrectable errors."},
}

// Prometheus renders a snapshot in the text exposition format. Labels are
// bounded (node, vendor, index, id, name): PIDs and pods never become
// labels, so cardinality stays flat.
func Prometheus(s collect.Snapshot) string {
	var b strings.Builder
	labels := func(d device.Device) string {
		node := d.Node
		if node == "" {
			node = s.Host
		}
		return fmt.Sprintf(`node=%q,vendor=%q,index="%d",id=%q,name=%q`, node, d.Vendor, d.Index, d.ID, d.Name)
	}
	for _, g := range gauges {
		var lines []string
		for _, d := range s.Devices {
			if v, ok := d.Metrics.Get(g.k); ok {
				lines = append(lines, fmt.Sprintf("%s{%s} %g", g.name, labels(d), v))
			}
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s\n", g.name, g.help, g.name, strings.Join(lines, "\n"))
	}
	b.WriteString("# HELP siltide_device_health_score Health score 0-100 derived by siltide.\n# TYPE siltide_device_health_score gauge\n")
	for _, d := range s.Devices {
		fmt.Fprintf(&b, "siltide_device_health_score{%s} %d\n", labels(d), d.Health)
	}
	b.WriteString("# HELP siltide_device_processes Processes holding the device.\n# TYPE siltide_device_processes gauge\n")
	for _, d := range s.Devices {
		fmt.Fprintf(&b, "siltide_device_processes{%s} %d\n", labels(d), len(d.Procs))
	}
	b.WriteString("# HELP siltide_device_idle_allocated Device has processes but sits idle (derived).\n# TYPE siltide_device_idle_allocated gauge\n")
	for _, d := range s.Devices {
		fmt.Fprintf(&b, "siltide_device_idle_allocated{%s} %d\n", labels(d), b2i(d.IdleAlloc))
	}
	if len(s.Energy) > 0 {
		b.WriteString("# HELP siltide_device_energy_kwh Energy since siltide started (derived).\n# TYPE siltide_device_energy_kwh counter\n")
		for _, d := range s.Devices {
			if en, ok := s.Energy[d.ID]; ok {
				fmt.Fprintf(&b, "siltide_device_energy_kwh{%s} %g\n", labels(d), en.KWh)
			}
		}
	}
	if len(s.Anomalies) > 0 {
		b.WriteString("# HELP siltide_device_anomaly Anomaly detected in recent history (derived).\n# TYPE siltide_device_anomaly gauge\n")
		for _, a := range s.Anomalies {
			fmt.Fprintf(&b, "siltide_device_anomaly{id=%q,kind=%q} 1\n", a.Device, a.Kind)
		}
	}
	b.WriteString("# HELP siltide_device_outlier Device trails its siblings (derived).\n# TYPE siltide_device_outlier gauge\n")
	for _, d := range s.Devices {
		fmt.Fprintf(&b, "siltide_device_outlier{%s} %d\n", labels(d), b2i(d.Outlier))
	}
	kinds := map[string]int{}
	for _, a := range s.Alerts {
		kinds[string(a.Severity)]++
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("# HELP siltide_alerts_active Active alerts by severity.\n# TYPE siltide_alerts_active gauge\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "siltide_alerts_active{severity=%q} %d\n", k, kinds[k])
	}
	if h := s.Host2; h != nil {
		if !h.CPU.Percent.Unknown() {
			fmt.Fprintf(&b, "# HELP siltide_host_cpu_percent Host CPU busy share.\n# TYPE siltide_host_cpu_percent gauge\nsiltide_host_cpu_percent %g\n", float64(h.CPU.Percent))
		}
		if !h.Mem.Used.Unknown() {
			fmt.Fprintf(&b, "# HELP siltide_host_memory_used_bytes Host memory in use.\n# TYPE siltide_host_memory_used_bytes gauge\nsiltide_host_memory_used_bytes %g\n", float64(h.Mem.Used))
		}
		if len(h.Nets) > 0 {
			b.WriteString("# HELP siltide_host_net_rx_bytes_per_second Interface receive rate.\n# TYPE siltide_host_net_rx_bytes_per_second gauge\n")
			for _, n := range h.Nets {
				fmt.Fprintf(&b, "siltide_host_net_rx_bytes_per_second{iface=%q} %g\n", n.Name, n.RxBps)
			}
		}
	}
	fmt.Fprintf(&b, "# HELP siltide_snapshot_age_seconds Seconds since the last collection.\n# TYPE siltide_snapshot_age_seconds gauge\nsiltide_snapshot_age_seconds %g\n", time.Since(s.Time).Seconds())
	return b.String()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
