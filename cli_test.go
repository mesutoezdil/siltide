package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moezdil/siltide/internal/config"
	"github.com/moezdil/siltide/internal/provider"
)

// fakeProviders are one vendor that answers and one that does not.
func fakeProviders(detected *int) []provider.Provider {
	return []provider.Provider{
		{Name: "here", Label: "Here (fake)", Detect: func() error { *detected++; return nil }},
		{Name: "gone", Label: "Gone (fake)", Hint: "install the gone driver", Detect: func() error { *detected++; return errors.New("no device") }},
	}
}

func TestDiagnoseReportsEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("SILTIDE_CONFIG", "")
	t.Setenv("SILTIDE_STATE_DIR", "")

	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("theme: nord\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatal(err)
	}

	// a drop-in beside the config file is listed too
	if err := os.MkdirAll(filepath.Join(dir, "config.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	dropIn := filepath.Join(dir, "config.d", "local.yaml")
	if err := os.WriteFile(dropIn, []byte("theme: gruvbox\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	detected := 0
	out := diagnose(cfg, cfgPath, fakeProviders(&detected), true)
	if !strings.Contains(out, dropIn) {
		t.Errorf("diagnose does not list the drop-in file:\n%s", out)
	}
	if detected != 2 {
		t.Fatalf("detect ran %d times, want 2", detected)
	}
	for _, want := range []string{
		"build", "version", "go ", "files", cfgPath,
		"state", "history", "settings", "theme", "nord",
		"providers", "here", "ok", "gone", "absent", "no device",
		"install the gone driver", "2 probed, 1 available",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnose output has no %q:\n%s", want, out)
		}
	}
}

func TestDiagnoseOfflineProbesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("SILTIDE_CONFIG", "")
	t.Setenv("SILTIDE_STATE_DIR", "")

	detected := 0
	out := diagnose(config.Default(), filepath.Join(dir, "missing.yaml"), fakeProviders(&detected), false)
	if detected != 0 {
		t.Fatalf("offline diagnose probed %d providers, want 0", detected)
	}
	if !strings.Contains(out, "skipped (--diagnose-offline)") {
		t.Errorf("offline output does not say detection was skipped:\n%s", out)
	}
	if !strings.Contains(out, "missing, defaults in use") {
		t.Errorf("a missing config file should be reported:\n%s", out)
	}
	if strings.Contains(out, "Here (fake)") {
		t.Errorf("offline output names a provider it never probed:\n%s", out)
	}
}

func TestDiagnoseFlagsAreCompletable(t *testing.T) {
	script, err := completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"--diagnose", "--diagnose-offline"} {
		if !strings.Contains(script, f) {
			t.Errorf("bash completion is missing %s", f)
		}
	}
}
