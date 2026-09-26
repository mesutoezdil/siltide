// Package mcp serves siltide's snapshot to agents over the Model Context
// Protocol. Everything here is a read: no tool changes a device, runs a
// command, or writes a file, which is what keeps the security model short.
//
// Two transports. Over stdio the client already holds the privileges it
// spawned siltide with, so there is nothing to authenticate. Over HTTP it is
// loopback and the same bearer token --listen uses.
package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/events"
	"github.com/moezdil/siltide/internal/query"
)

// Version is the protocol revision this server speaks.
const Version = "2024-11-05"

// Server answers Model Context Protocol calls from one engine.
type Server struct {
	eng  *collect.Engine
	name string
}

// New returns a server over eng. name is what the client shows.
func New(eng *collect.Engine, name string) *Server { return &Server{eng: eng, name: name} }

// Request is a JSON-RPC 2.0 request. A missing id makes it a notification,
// which takes no reply.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the error member of a response.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Tool is one thing an agent can ask for.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Handle answers one request. A notification returns nil, which the
// transports take as "write nothing".
func (s *Server) Handle(req Request) *Response {
	notification := len(req.ID) == 0
	reply := func(result any, err *RPCError) *Response {
		if notification {
			return nil
		}
		return &Response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: err}
	}

	switch req.Method {
	case "initialize":
		return reply(map[string]any{
			"protocolVersion": Version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "siltide", "version": s.name},
			"instructions": "siltide reports accelerators: GPUs, NPUs and anything else a machine calls one. " +
				"Everything here is read-only. list_devices and list_processes take the same filter language the " +
				"interface does, so util>80, !vendor:nvidia and ns:ml all work.",
		}, nil)
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "ping":
		return reply(map[string]any{}, nil)
	case "tools/list":
		return reply(map[string]any{"tools": Tools()}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return reply(nil, &RPCError{Code: -32602, Message: "invalid params: " + err.Error()})
		}
		text, err := s.call(p.Name, p.Arguments)
		if err != nil {
			// A tool that could not answer reports it as a result, not as a
			// protocol error: the model is meant to read it and try again.
			return reply(map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}, nil)
		}
		return reply(map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, nil)
	}
	return reply(nil, &RPCError{Code: -32601, Message: "no method " + req.Method})
}

// Tools is everything an agent may call, with the arguments each takes.
func Tools() []Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	filterArg := str("the filter language the interface uses: free words, key:value (dev:3, ns:ml, vendor:nvidia), " +
		"comparisons (util>80, temp>=70, mem<50, mem_used>8G), and ! to negate any of them")

	return []Tool{
		{
			Name: "fleet_summary",
			Description: "Totals for every device siltide can see: how many are busy, idle, allocated or down, " +
				"the power draw, the memory in use, the worst health score, and how many alerts are firing. " +
				"Start here.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name: "list_devices",
			Description: "One line per device: index, vendor, name, utilization, memory, temperature, power, " +
				"state, health and how many processes hold it. Narrow it with a filter.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"filter": filterArg}},
		},
		{
			Name:        "device_detail",
			Description: "Every metric siltide has for one device, with its health notes, interconnect links, topology and the processes on it.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"device": str("the index, or the id, of the device")},
				"required":   []string{"device"},
			},
		},
		{
			Name: "list_processes",
			Description: "The processes holding devices, with their user, memory, utilization and, where Kubernetes " +
				"is present, the pod, namespace and workload they belong to.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"filter": filterArg}},
		},
		{
			Name:        "device_history",
			Description: "Recent values of one metric for one device, oldest first, from the history siltide keeps on disk.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"device": str("the index, or the id, of the device"),
					"metric": str("util, mem_used, temp, power or clock_core"),
					"points": num("how many samples to return, newest last (default 60)"),
				},
				"required": []string{"device"},
			},
		},
		{
			Name:        "recent_events",
			Description: "What siltide noticed: thermal warnings, ECC errors, Xids, devices appearing and disappearing, and rules from the config.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"severity": str("only events at this severity or worse: info, warning, critical"),
					"limit":    num("how many events, newest first (default 20)"),
				},
			},
		},
		{
			Name: "health_report",
			Description: "Every device whose health is below full, with the reasons siltide derived, plus the alerts " +
				"firing now and the devices that are allocated but idle.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// call runs one tool and renders its answer as text, which is what a model
// reads. JSON would be more precise and much harder to reason about aloud.
func (s *Server) call(name string, args json.RawMessage) (string, error) {
	var a struct {
		Filter   string `json:"filter"`
		Device   string `json:"device"`
		Metric   string `json:"metric"`
		Points   int    `json:"points"`
		Severity string `json:"severity"`
		Limit    int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("arguments: %w", err)
		}
	}
	snap := s.eng.Snapshot()

	switch name {
	case "fleet_summary":
		return fleetSummary(snap), nil
	case "list_devices":
		return listDevices(snap, a.Filter)
	case "device_detail":
		return deviceDetail(snap, a.Device)
	case "list_processes":
		return listProcesses(snap, a.Filter)
	case "device_history":
		return s.deviceHistory(snap, a.Device, a.Metric, a.Points)
	case "recent_events":
		return recentEvents(s.eng.Events(), a.Severity, a.Limit), nil
	case "health_report":
		return healthReport(snap), nil
	}
	return "", fmt.Errorf("no tool called %q; call tools/list for what there is", name)
}

func fleetSummary(s collect.Snapshot) string {
	f := s.Fleet
	var b strings.Builder
	fmt.Fprintf(&b, "host %s at %s\n", s.Host, s.Time.Format("2006-01-02 15:04:05"))
	if s.Demo {
		b.WriteString("this is the simulated fleet, not real hardware\n")
	}
	fmt.Fprintf(&b, "devices %d: %d busy, %d active, %d idle, %d down\n", f.Devices, f.Busy, f.Active, f.Idle, f.Down)
	fmt.Fprintf(&b, "allocated %d, of which idle %d, outliers %d\n", f.Allocated, f.IdleAlloc, f.Outliers)
	if f.MemTotal > 0 {
		fmt.Fprintf(&b, "memory %s of %s (%.0f%%)\n", size(f.MemUsed), size(f.MemTotal), f.MemUsed/f.MemTotal*100)
	}
	if f.PowerW > 0 {
		fmt.Fprintf(&b, "power %.0fW", f.PowerW)
		if f.PowerCapW > 0 {
			fmt.Fprintf(&b, " of %.0fW capped", f.PowerCapW)
		}
		b.WriteString("\n")
	}
	if f.Unused >= 0.5 {
		fmt.Fprintf(&b, "utilization headroom on allocated devices: %.1f devices' worth\n", f.Unused)
	}
	worst, id := 100, ""
	for _, d := range s.Devices {
		if d.Health < worst {
			worst, id = d.Health, d.Label()
		}
	}
	if id != "" {
		fmt.Fprintf(&b, "worst health %d on device %s\n", worst, id)
	}
	fmt.Fprintf(&b, "alerts firing %d\n", len(s.Alerts))
	for _, n := range s.KubeNodes {
		for name, r := range n.Resources {
			fmt.Fprintf(&b, "scheduler: node %s has %d %s allocatable, %d requested by %d pods\n",
				n.Name, r.Allocatable, name, r.Requested, r.Pods)
		}
	}
	for _, w := range s.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	return b.String()
}

func listDevices(s collect.Snapshot, filter string) (string, error) {
	f := query.ParseFilter(filter)
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s %-10s %-28s %6s %10s %6s %7s %-8s %6s %5s\n",
		"DEVICE", "VENDOR", "NAME", "UTIL", "MEMORY", "TEMP", "POWER", "STATE", "HEALTH", "PROCS")
	n := 0
	for _, d := range s.Devices {
		if !f.Device(d) {
			continue
		}
		n++
		fmt.Fprintf(&b, "%-8s %-10s %-28s %6s %10s %6s %7s %-8s %6d %5d\n",
			d.Label(), d.Vendor, trunc(d.Name, 28), pct(d.Metrics, device.Util), mem(d),
			val(d.Metrics, device.Temp, "°C"), val(d.Metrics, device.Power, "W"),
			d.State, d.Health, len(d.Procs))
	}
	if n == 0 {
		return "no device matches that filter (" + strconv.Itoa(len(s.Devices)) + " devices in total)", nil
	}
	return b.String(), nil
}

func deviceDetail(s collect.Snapshot, want string) (string, error) {
	d, ok := find(s, want)
	if !ok {
		return "", fmt.Errorf("no device %q; list_devices shows what there is", want)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "device %s: %s (%s), state %s, health %d\n", d.Label(), d.Name, d.Vendor, d.State, d.Health)
	if d.Allocated != "" {
		fmt.Fprintf(&b, "held by %s\n", d.Allocated)
	}
	if len(d.HealthNotes) > 0 {
		fmt.Fprintf(&b, "health notes: %s\n", strings.Join(d.HealthNotes, "; "))
	}
	if d.IdleAlloc {
		b.WriteString("this device has processes on it and is doing nothing\n")
	}
	b.WriteString("\nmetrics:\n")
	for _, k := range d.Metrics.Keys() {
		fmt.Fprintf(&b, "  %-18s %s\n", k, metricText(k, d.Metrics[k]))
	}
	if len(d.Links) > 0 {
		b.WriteString("\ninterconnect:\n")
		for _, l := range d.Links {
			state := "down"
			if l.Active {
				state = "up"
			}
			fmt.Fprintf(&b, "  link %d %s %s peer %s errors %d\n", l.Index, state, l.Version, l.Peer, l.Errors)
		}
	}
	if len(d.Procs) > 0 {
		b.WriteString("\nprocesses:\n")
		for _, p := range d.Procs {
			fmt.Fprintf(&b, "  pid %-8d %-16s user %-10s mem %-10s util %-6s %s\n",
				p.PID, trunc(p.Name, 16), orNA(p.User), val(p.Metrics, device.MemUsed, ""), pct(p.Metrics, device.Util), where(p))
		}
	}
	return b.String(), nil
}

func listProcesses(s collect.Snapshot, filter string) (string, error) {
	f := query.ParseFilter(filter)
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s %-10s %-18s %-8s %10s %6s %s\n", "PID", "USER", "PROCESS", "DEVICE", "MEMORY", "UTIL", "POD / JOB / COMMAND")
	n := 0
	for _, d := range s.Devices {
		for _, p := range d.Procs {
			if !f.Process(d, p) {
				continue
			}
			n++
			fmt.Fprintf(&b, "%-8d %-10s %-18s %-8s %10s %6s %s\n",
				p.PID, orNA(p.User), trunc(p.Name, 18), d.Label(),
				val(p.Metrics, device.MemUsed, ""), pct(p.Metrics, device.Util), where(p))
		}
	}
	if n == 0 {
		return "no process matches that filter", nil
	}
	return b.String(), nil
}

func (s *Server) deviceHistory(snap collect.Snapshot, want, metric string, points int) (string, error) {
	d, ok := find(snap, want)
	if !ok {
		return "", fmt.Errorf("no device %q; list_devices shows what there is", want)
	}
	h := s.eng.History()
	if h == nil {
		return "", fmt.Errorf("history is off, so there is nothing to look back through")
	}
	if metric == "" {
		metric = "util"
	}
	if points <= 0 {
		points = 60
	}
	k := device.Metric(metric)
	vals := h.Recent(d.ID, k, points)
	if len(vals) == 0 {
		return fmt.Sprintf("no history for %s on device %s yet", metric, d.Label()), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "device %s, %s, %d samples oldest first, one per %s:\n", d.Label(), metric, len(vals), h.Resolution())
	for i, v := range vals {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(metricText(k, v))
	}
	b.WriteString("\n")
	return b.String(), nil
}

func recentEvents(evs []events.Event, severity string, limit int) string {
	if limit <= 0 {
		limit = 20
	}
	floor := rank(events.Severity(severity))
	var keep []events.Event
	for _, e := range evs {
		if rank(e.Severity) >= floor {
			keep = append(keep, e)
		}
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].Time.After(keep[j].Time) })
	if len(keep) > limit {
		keep = keep[:limit]
	}
	if len(keep) == 0 {
		return "no events"
	}
	var b strings.Builder
	for _, e := range keep {
		fmt.Fprintf(&b, "%s %-8s %-10s %-10s %s\n", e.Time.Format("15:04:05"), e.Severity, orNA(e.Label), e.Kind, e.Message)
	}
	return b.String()
}

func healthReport(s collect.Snapshot) string {
	var b strings.Builder
	if len(s.Alerts) > 0 {
		b.WriteString("alerts firing now:\n")
		for _, a := range s.Alerts {
			fmt.Fprintf(&b, "  %-8s %-10s %s\n", a.Severity, orNA(a.Label), a.Message)
		}
		b.WriteString("\n")
	}
	hurt := 0
	for _, d := range s.Devices {
		if d.Health >= 100 && !d.IdleAlloc && !d.Outlier {
			continue
		}
		hurt++
		fmt.Fprintf(&b, "device %s health %d", d.Label(), d.Health)
		if len(d.HealthNotes) > 0 {
			fmt.Fprintf(&b, ": %s", strings.Join(d.HealthNotes, "; "))
		}
		if d.IdleAlloc {
			b.WriteString("; allocated but idle")
		}
		if d.Outlier {
			b.WriteString("; below its siblings")
		}
		b.WriteString("\n")
	}
	if hurt == 0 && len(s.Alerts) == 0 {
		return "every device reports full health, nothing is allocated and idle, and no alert is firing"
	}
	return b.String()
}
