// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"strings"
	"testing"
)

func TestCheckArgs(t *testing.T) {
	t.Parallel()
	schema := object([]string{"brand"}, map[string]any{
		"brand":  str(brandDesc),
		"limit":  map[string]any{"type": "integer"},
		"fit":    enum("how to fit", "truncate", "thread"),
		"media":  strs("media IDs"),
		"live":   map[string]any{"type": "boolean"},
		"extras": map[string]any{"type": "object"},
	})
	tests := []struct {
		name string
		args map[string]any
		want string // a substring of the error; empty for none
	}{
		{"valid", map[string]any{"brand": "araldo", "limit": float64(5), "fit": "thread", "media": []any{"media_1"}, "live": true,
			"extras": map[string]any{"a": "b"}}, ""},
		{"a misspelled argument", map[string]any{"brand": "araldo", "limt": float64(5)}, `unknown argument "limt"; this tool takes brand, extras, fit, limit, live, media`},
		{"missing", map[string]any{"limit": float64(5)}, "brand is required"},
		{"empty", map[string]any{"brand": ""}, "brand is required"},
		{"a word for a number", map[string]any{"brand": "araldo", "limit": "two"}, "limit must be an integer"},
		{"a fraction for an integer", map[string]any{"brand": "araldo", "limit": 1.5}, "limit must be an integer"},
		{"outside the enum", map[string]any{"brand": "araldo", "fit": "squash"}, `fit must be one of truncate, thread, not "squash"`},
		{"a string for a list", map[string]any{"brand": "araldo", "media": "media_1"}, "media must be an array"},
		{"a number in a list of strings", map[string]any{"brand": "araldo", "media": []any{float64(1)}}, "media[0] must be a string"},
		{"a string for a boolean", map[string]any{"brand": "araldo", "live": "yes"}, "live must be a boolean"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkArgs(schema, tt.args)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("checkArgs = %v, want none", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("checkArgs = %v, want %q", err, tt.want)
			}
		})
	}
}

// Every tool's schema is one checkArgs can hold calls to: an object that
// refuses unknown arguments, whose required arguments it declares.
func TestToolSchemasAreStrict(t *testing.T) {
	t.Parallel()
	for _, tool := range NewServer(nil).tools {
		if tool.Input["type"] != "object" || tool.Input["additionalProperties"] != false {
			t.Errorf("%s: the schema does not refuse unknown arguments", tool.Name)
		}
		props, _ := tool.Input["properties"].(map[string]any)
		required, _ := tool.Input["required"].([]string)
		for _, r := range required {
			if _, ok := props[r]; !ok {
				t.Errorf("%s: requires %q but does not declare it", tool.Name, r)
			}
		}
		if err := checkArgs(tool.Input, map[string]any{"definitely_not_an_argument": "x"}); err == nil {
			t.Errorf("%s: accepted an unknown argument", tool.Name)
		}
	}
}
