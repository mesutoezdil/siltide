package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/moezdil/siltide/internal/config"
)

// TestSchemaMatchesTheFileInTheRepository is what stops the published schema
// from drifting away from the struct that reads the file. Regenerate with
// `go run ./scripts/schema > schemas/config.schema.json`.
func TestSchemaMatchesTheFileInTheRepository(t *testing.T) {
	out, err := exec.Command("go", "run", ".").Output()
	if err != nil {
		t.Fatalf("generating: %v", err)
	}
	have, err := os.ReadFile("../../schemas/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != strings.TrimSpace(string(have)) {
		t.Error("schemas/config.schema.json is not what ./scripts/schema produces; regenerate it")
	}
}

func TestSchemaCoversEveryKey(t *testing.T) {
	var s map[string]any
	b, err := os.ReadFile("../../schemas/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	props, _ := s["properties"].(map[string]any)

	// every yaml key of the struct is in the schema
	rt := reflect.TypeOf(config.Default())
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		if _, ok := props[name]; !ok {
			t.Errorf("the schema is missing %q", name)
		}
	}

	// and it is closed, because siltide rejects what it does not know
	if s["additionalProperties"] != false {
		t.Error("the schema accepts unknown keys; siltide does not")
	}

	// a duration is a string with a unit, not a count of nanoseconds
	refresh, _ := props["refresh"].(map[string]any)
	if refresh["type"] != "string" {
		t.Errorf("refresh is typed %v", refresh["type"])
	}
}
