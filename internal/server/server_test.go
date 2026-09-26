package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moezdil/siltide/internal/collect"
	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/provider/sim"
)

func TestServer(t *testing.T) {
	e := collect.New([]provider.Provider{sim.Provider(2)}, config.Default(), nil, true)
	e.Detect()
	e.Collect(context.Background())
	srv := httptest.NewServer(Handler(e, "secret"))
	defer srv.Close()

	get := func(path, token string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, b.String()
	}
	if code, _ := get("/api/snapshot", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := get("/healthz", ""); code != http.StatusOK {
		t.Fatalf("healthz needs no token: %d", code)
	}
	code, body := get("/metrics", "secret")
	if code != http.StatusOK || !strings.Contains(body, `siltide_device_util_percent{node=`) || !strings.Contains(body, "siltide_device_health_score") {
		t.Fatalf("metrics %d %s", code, body[:min(len(body), 300)])
	}
	if code, body := get("/api/snapshot", "secret"); code != http.StatusOK || !strings.Contains(body, `"devices"`) {
		t.Fatalf("snapshot %d", code)
	}
}

// TestMetricsNamesAreDeclared checks every sample against the HELP and TYPE
// lines around it. A renamed metric that only half landed shows up here as a
// sample Prometheus would read as an untyped metric of its own.
func TestMetricsNamesAreDeclared(t *testing.T) {
	e := collect.New([]provider.Provider{sim.Provider(2)}, config.Default(), nil, true)
	e.Detect()
	snap := e.Collect(context.Background())
	if snap.Host2 == nil {
		t.Skip("no host counters on this machine")
	}

	declared, samples := map[string]bool{}, 0
	for _, line := range strings.Split(Prometheus(snap), "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "# HELP "), strings.HasPrefix(line, "# TYPE "):
			declared[strings.Fields(line)[2]] = true
		default:
			name, _, _ := strings.Cut(line, " ")
			name, _, _ = strings.Cut(name, "{")
			samples++
			if !declared[name] {
				t.Errorf("sample %q has no HELP or TYPE above it", name)
			}
			if !strings.HasPrefix(name, "siltide_") {
				t.Errorf("metric %q is not in the siltide namespace", name)
			}
		}
	}
	if samples == 0 {
		t.Fatal("no samples in the metrics output")
	}
}
