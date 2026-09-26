package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/moezdil/siltide/internal/events"
)

func TestPush(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
	}))
	defer srv.Close()
	n := New(Options{Webhook: srv.URL, MinSeverity: "warning", Resend: time.Hour, Host: "h"})
	alerts := []events.Event{
		{Severity: events.Warning, Kind: "thermal", Device: "d0", Label: "0", Message: "hot"},
		{Severity: events.Info, Kind: "idle-allocated", Device: "d1", Label: "1", Message: "idle"},
	}
	n.Push(context.Background(), alerts)
	n.Push(context.Background(), alerts) // active and not due: no second send
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("sent %d payloads, want 1", len(got))
	}
	if list := got[0]["alerts"].([]any); len(list) != 1 {
		t.Fatalf("info alert must be filtered: %v", list)
	}
	if New(Options{}) != nil {
		t.Fatal("no outputs means no notifier")
	}
}
