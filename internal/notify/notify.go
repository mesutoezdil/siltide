// Package notify pushes alerts out of the terminal: a generic JSON webhook,
// Slack incoming webhooks, and Prometheus Alertmanager.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/moezdil/siltide/internal/events"
)

// Options are the alert outputs from the config.
type Options struct {
	Webhook      string
	Slack        string
	Alertmanager string
	MinSeverity  string
	Resend       time.Duration
	Host         string
}

// Notifier remembers what was sent so an active alert is not repeated
// on every pass.
type Notifier struct {
	o      Options
	client *http.Client
	mu     sync.Mutex
	sent   map[string]time.Time // device/kind: last send
	errs   int
	last   error
}

// New returns a notifier; nil when no output is configured.
func New(o Options) *Notifier {
	if o.Webhook == "" && o.Slack == "" && o.Alertmanager == "" {
		return nil
	}
	if o.Resend <= 0 {
		o.Resend = time.Hour
	}
	return &Notifier{o: o, client: &http.Client{Timeout: 5 * time.Second}, sent: map[string]time.Time{}}
}

// SetOptions applies reloaded delivery settings, keeping what was already
// sent so a reload does not repeat every active alert.
func (n *Notifier) SetOptions(o Options) {
	if n == nil {
		return
	}
	if o.Resend <= 0 {
		o.Resend = time.Hour
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.o = o
}

var rank = map[events.Severity]int{events.Info: 0, events.Warning: 1, events.Critical: 2}

// Push sends every alert that is new or due for a resend and forgets alerts
// that cleared.
func (n *Notifier) Push(ctx context.Context, alerts []events.Event) {
	if n == nil {
		return
	}
	n.mu.Lock()
	now := time.Now()
	active := map[string]bool{}
	var due []events.Event
	for _, a := range alerts {
		if rank[a.Severity] < rank[events.Severity(n.o.MinSeverity)] {
			continue
		}
		key := a.Device + "/" + a.Kind
		active[key] = true
		if last, ok := n.sent[key]; !ok || now.Sub(last) >= n.o.Resend {
			due = append(due, a)
			n.sent[key] = now
		}
	}
	for key := range n.sent {
		if !active[key] {
			delete(n.sent, key)
		}
	}
	n.mu.Unlock()
	if len(due) == 0 {
		return
	}
	go n.deliver(ctx, due)
}

func (n *Notifier) deliver(ctx context.Context, alerts []events.Event) {
	var err error
	if n.o.Webhook != "" {
		err = n.post(ctx, n.o.Webhook, map[string]any{"host": n.o.Host, "alerts": alerts})
	}
	if n.o.Slack != "" {
		var lines []string
		for _, a := range alerts {
			lines = append(lines, fmt.Sprintf("%s *%s* #%s %s", icon(a.Severity), a.Severity, a.Label, a.Message))
		}
		if e := n.post(ctx, n.o.Slack, map[string]any{"text": fmt.Sprintf("siltide on %s\n%s", n.o.Host, strings.Join(lines, "\n"))}); e != nil {
			err = e
		}
	}
	if n.o.Alertmanager != "" {
		var payload []map[string]any
		for _, a := range alerts {
			payload = append(payload, map[string]any{
				"labels":      map[string]string{"alertname": "siltide_" + a.Kind, "severity": string(a.Severity), "instance": n.o.Host, "device": a.Device, "label": a.Label},
				"annotations": map[string]string{"summary": a.Message},
				"startsAt":    a.Time.UTC().Format(time.RFC3339),
			})
		}
		if e := n.post(ctx, strings.TrimRight(n.o.Alertmanager, "/")+"/api/v2/alerts", payload); e != nil {
			err = e
		}
	}
	n.mu.Lock()
	n.last = err
	if err != nil {
		n.errs++
	}
	n.mu.Unlock()
}

func (n *Notifier) post(ctx context.Context, url string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}

func icon(s events.Severity) string {
	switch s {
	case events.Critical:
		return ":red_circle:"
	case events.Warning:
		return ":warning:"
	}
	return ":information_source:"
}

// Status reports delivery failures for the Health tab.
func (n *Notifier) Status() (failures int, last error) {
	if n == nil {
		return 0, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.errs, n.last
}
