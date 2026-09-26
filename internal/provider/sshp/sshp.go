// Package sshp reads accelerators on hosts where siltide is not installed by
// running the vendor tools over the system ssh client. Nothing to deploy
// on the far side: the node needs its vendor driver and an ssh login.
package sshp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/provider"
	"github.com/moezdil/siltide/internal/provider/smi"
)

// Providers returns one provider per CLI vendor for target ("user@host" or
// "user@host:port"). key is an optional private key file.
func Providers(name, target, key string) []provider.Provider {
	host, port := target, ""
	if i := strings.LastIndex(target, ":"); i > 0 && !strings.Contains(target[i:], "]") {
		host, port = target[:i], target[i+1:]
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "StrictHostKeyChecking=accept-new", "-T"}
	if port != "" {
		args = append(args, "-p", port)
	}
	if key != "" {
		args = append(args, "-i", key)
	}
	args = append(args, host)
	run := func(ctx context.Context, cmdline string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "ssh", append(append([]string{}, args...), cmdline)...)
		out, err := cmd.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return nil, fmt.Errorf("ssh %s: %s", host, strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, fmt.Errorf("ssh %s: %w", host, err)
		}
		return out, nil
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return []provider.Provider{{Name: "node:" + name, Label: "ssh " + target, Detect: func() error { return errors.New("ssh client not installed") }}}
	}
	return smi.Remote(name, run)
}
