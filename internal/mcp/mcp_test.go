package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/history"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/provider/sim"
)

// demoServer is a server over the simulated fleet, with history so the
// history tool has something to answer with.
func demoServer(t *testing.T) *Server {
	t.Helper()
	h, err := history.Open(history.Options{Keep: time.Hour, Resolution: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	e := collect.New([]provider.Provider{sim.Provider(4)}, config.Default(), h, true)
	t.Cleanup(e.Close)
	e.Detect()
	e.Collect(context.Background())
	time.Sleep(2 * time.Millisecond)
	e.Collect(context.Background())
	return New(e, "test")
}

// ask makes one request and returns the result as a map.
func ask(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.Handle(Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: raw})
	if resp == nil {
		t.Fatalf("%s got no reply", method)
	}
	if resp.Error != nil {
		t.Fatalf("%s: %s", method, resp.Error.Message)
	}
	out, _ := resp.Result.(map[string]any)
	return out
}

// text is the text a tool call came back with.
func text(t *testing.T, s *Server, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res := ask(t, s, "tools/call", map[string]any{"name": tool, "arguments": args})
	content, _ := res["content"].([]map[string]any)
	if len(content) == 0 {
		t.Fatalf("%s returned no content: %+v", tool, res)
	}
	isErr, _ := res["isError"].(bool)
	return content[0]["text"].(string), isErr
}

func TestHandshakeAndTools(t *testing.T) {
	s := demoServer(t)

	init := ask(t, s, "initialize", map[string]any{"protocolVersion": Version})
	if init["protocolVersion"] != Version {
		t.Errorf("protocol %v", init["protocolVersion"])
	}
	if info, _ := init["serverInfo"].(map[string]any); info["name"] != "siltide" {
		t.Errorf("serverInfo %+v", init["serverInfo"])
	}

	// a notification is answered by saying nothing at all
	if resp := s.Handle(Request{JSONRPC: "2.0", Method: "notifications/initialized"}); resp != nil {
		t.Errorf("a notification was answered: %+v", resp)
	}

	list := ask(t, s, "tools/list", map[string]any{})
	tools, _ := list["tools"].([]Tool)
	if len(tools) < 7 {
		t.Fatalf("%d tools", len(tools))
	}
	for _, tool := range tools {
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %q has no object schema", tool.Name)
		}
		// Every tool must run with no arguments at all, or say what it needs.
		if _, isErr := text(t, s, tool.Name, nil); isErr {
			req, _ := tool.InputSchema["required"].([]string)
			if len(req) == 0 {
				t.Errorf("tool %q fails with no arguments but marks none required", tool.Name)
			}
		}
	}

	if resp := s.Handle(Request{JSONRPC: "2.0", ID: json.RawMessage(`9`), Method: "nope"}); resp.Error == nil {
		t.Error("an unknown method should be an error")
	}
}

func TestToolsAnswerFromTheSnapshot(t *testing.T) {
	s := demoServer(t)

	summary, _ := text(t, s, "fleet_summary", nil)
	for _, want := range []string{"devices", "allocated", "alerts firing", "simulated"} {
		if !strings.Contains(summary, want) {
			t.Errorf("fleet_summary has no %q:\n%s", want, summary)
		}
	}

	devices, _ := text(t, s, "list_devices", nil)
	if !strings.Contains(devices, "nvidia") || !strings.Contains(devices, "HEALTH") {
		t.Errorf("list_devices:\n%s", devices)
	}

	// the filter language is the same one the interface takes
	busy, _ := text(t, s, "list_devices", map[string]any{"filter": "util>1000"})
	if !strings.Contains(busy, "no device matches") {
		t.Errorf("an impossible filter returned:\n%s", busy)
	}
	amd, _ := text(t, s, "list_devices", map[string]any{"filter": "!vendor:nvidia"})
	if strings.Contains(amd, "nvidia ") {
		t.Errorf("a negated vendor filter let nvidia through:\n%s", amd)
	}

	detail, _ := text(t, s, "device_detail", map[string]any{"device": "0"})
	for _, want := range []string{"device 0", "metrics:", "util"} {
		if !strings.Contains(detail, want) {
			t.Errorf("device_detail has no %q:\n%s", want, detail)
		}
	}
	missing, isErr := text(t, s, "device_detail", map[string]any{"device": "does-not-exist"})
	if !isErr || !strings.Contains(missing, "no device") {
		t.Errorf("an unknown device gave %q (error %v)", missing, isErr)
	}

	procs, _ := text(t, s, "list_processes", nil)
	if !strings.Contains(procs, "PID") {
		t.Errorf("list_processes:\n%s", procs)
	}

	hist, _ := text(t, s, "device_history", map[string]any{"device": "0", "metric": "util", "points": 5})
	if !strings.Contains(hist, "device 0") || !strings.Contains(hist, "util") {
		t.Errorf("device_history:\n%s", hist)
	}

	events, _ := text(t, s, "recent_events", map[string]any{"limit": 5})
	if events == "" {
		t.Error("recent_events said nothing at all")
	}

	health, _ := text(t, s, "health_report", nil)
	if health == "" {
		t.Error("health_report said nothing at all")
	}

	// an unknown tool is an error the model can read, not a protocol failure
	if out, isErr := text(t, s, "make_coffee", nil); !isErr || !strings.Contains(out, "no tool") {
		t.Errorf("unknown tool gave %q (error %v)", out, isErr)
	}
}

func TestServeStdio(t *testing.T) {
	s := demoServer(t)
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`not json at all`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fleet_summary","arguments":{}}}`,
		"",
	}, "\n"))

	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), in, &out, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines for two requests and one bad one:\n%s", len(lines), out.String())
	}
	var parseErr Response
	if err := json.Unmarshal([]byte(lines[1]), &parseErr); err != nil {
		t.Fatal(err)
	}
	if parseErr.Error == nil || parseErr.Error.Code != -32700 {
		t.Errorf("a line that is not JSON gave %+v", parseErr.Error)
	}
	if !strings.Contains(lines[2], "devices") {
		t.Errorf("the tool call did not answer: %s", lines[2])
	}
}

func TestHTTPTransport(t *testing.T) {
	s := demoServer(t)
	srv := httptest.NewServer(s.Handler("secret"))
	defer srv.Close()

	post := func(token, host, body string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fleet_summary","arguments":{}}}`
	resp := post("secret", "", call)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Error != nil {
		t.Fatalf("error %+v", out.Error)
	}

	if r := post("wrong", "", call); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong token gave %d", r.StatusCode)
	}
	if r := post("", "", call); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token gave %d", r.StatusCode)
	}
	// A page on this machine must not be able to point a browser at it.
	if r := post("secret", "siltide.example.com", call); r.StatusCode != http.StatusForbidden {
		t.Errorf("a foreign Host gave %d", r.StatusCode)
	}

	get, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = get.Body.Close() }()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET gave %d", get.StatusCode)
	}
}

func TestServeRefusesANonLoopbackAddress(t *testing.T) {
	s := demoServer(t)
	if err := s.Serve(context.Background(), "0.0.0.0:9999", ""); err == nil {
		t.Fatal("serving on every interface should be refused")
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if !loopbackAddr(addr) {
			t.Errorf("%s should count as loopback", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:80", "192.168.1.5:80", "example.com:80", "nonsense"} {
		if loopbackAddr(addr) {
			t.Errorf("%s should not count as loopback", addr)
		}
	}
}

// Typing at the stdio server gets "parse error" for every line, which says
// what happened and not what to do. One line of English goes to the hint
// writer, once, and never to the protocol stream.
func TestServeStdioHintsOnceWhenTheInputIsNotJSON(t *testing.T) {
	s := demoServer(t)
	in := strings.NewReader("masada miyiz\nneden\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	var out, hint bytes.Buffer
	if err := s.ServeStdio(context.Background(), in, &out, &hint); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(hint.String(), "not JSON"); n != 1 {
		t.Fatalf("hinted %d times, want once:\n%s", n, hint.String())
	}
	if strings.Contains(out.String(), "not JSON") {
		t.Fatal("the hint reached the protocol stream")
	}
	if n := strings.Count(out.String(), "parse error"); n != 2 {
		t.Fatalf("want a parse error for each of the two bad lines, got %d", n)
	}
	if !strings.Contains(out.String(), "fleet_summary") {
		t.Fatal("the request after the bad lines went unanswered")
	}
}

// A pipe from an agent passes no hint writer, and then nothing but protocol
// is produced no matter what arrives.
func TestServeStdioStaysSilentWithoutAHintWriter(t *testing.T) {
	s := demoServer(t)
	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), strings.NewReader("hello\n"), &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "parse error") || strings.Contains(out.String(), "not JSON") {
		t.Fatalf("out is %q", out.String())
	}
}
