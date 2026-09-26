// Package remote reads devices from another siltide running with `--listen`, so
// one terminal can watch a whole fleet without an agent or a database.
package remote

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/device"
	"github.com/moezdil/siltide/internal/provider"
)

// Options describe one node.
type Options struct {
	Name      string
	URL       string
	Token     string
	TokenFile string // read at start; wins over Token
	CA        string // PEM file that signed the node's certificate
}

// Provider polls one node's `/api/snapshot`.
func Provider(o Options) provider.Provider {
	url := strings.TrimRight(o.URL, "/")
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	token := o.Token
	if o.TokenFile != "" {
		if b, err := os.ReadFile(o.TokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if o.CA != "" {
		if pem, err := os.ReadFile(o.CA); err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
		}
	}
	get := func(ctx context.Context, path string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+path, nil)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d", url+path, resp.StatusCode)
		}
		if out == nil {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return provider.Provider{
		Name: "node:" + o.Name, Label: "siltide on " + o.Name + " (" + url + ")",
		Hint: "Start siltide with --listen on that node and make sure the port is reachable.",
		Detect: func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return get(ctx, "/healthz", nil)
		},
		Read: func(ctx context.Context) ([]device.Device, error) {
			var snap struct {
				Devices []device.Device `json:"devices"`
			}
			if err := get(ctx, "/api/snapshot", &snap); err != nil {
				return nil, err
			}
			if snap.Devices == nil {
				return nil, errors.New("empty snapshot")
			}
			for i := range snap.Devices {
				d := &snap.Devices[i]
				d.Node = o.Name
				d.ID = o.Name + "/" + d.ID
				if d.Parent != "" {
					d.Parent = o.Name + "/" + d.Parent
				}
			}
			return snap.Devices, nil
		},
	}
}
