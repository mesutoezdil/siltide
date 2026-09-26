// Command schema writes the JSON Schema for config.yaml by reflecting over
// the struct that reads it, so the two cannot drift: a field added to
// internal/config appears here on the next run, and CI fails when the file
// in the repository is not what this produces.
//
//	go run ./scripts/schema > schemas/config.schema.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/moezdil/siltide/internal/config"
)

func main() {
	s := object(reflect.TypeOf(config.Default()))
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["$id"] = "https://raw.githubusercontent.com/moezdil/siltide/main/schemas/config.schema.json"
	s["title"] = "siltide configuration"
	s["description"] = "Every key of ~/.config/siltide/config.yaml. Unknown keys are rejected by siltide itself, so this schema is closed too."
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "schema:", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
}

// object turns a struct into a schema, keyed by the yaml names siltide reads.
func object(t reflect.Type) map[string]any {
	props := map[string]any{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" || !f.IsExported() {
			continue
		}
		prop := schemaFor(f.Type)
		if doc := strings.TrimSpace(f.Tag.Get("doc")); doc != "" {
			prop["description"] = doc
		}
		props[name] = prop
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false, // siltide rejects unknown keys; so does this
	}
}

// schemaFor maps one Go type onto its YAML shape.
func schemaFor(t reflect.Type) map[string]any {
	if t == reflect.TypeOf(time.Duration(0)) {
		// A duration is written the way Go parses it, never as a number of
		// nanoseconds, which is what a bare integer would mean here.
		return map[string]any{
			"type":        "string",
			"pattern":     `^\d+(\.\d+)?(ns|us|µs|ms|s|m|h)([\d.]+(ns|us|µs|ms|s|m|h))*$`,
			"description": "a duration such as 2s, 500ms or 24h",
		}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaFor(t.Elem())
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schemaFor(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaFor(t.Elem())}
	case reflect.Struct:
		return object(t)
	}
	return map[string]any{} // anything else takes whatever it is given
}
