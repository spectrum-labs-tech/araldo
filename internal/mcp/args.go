// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// checkArgs holds a tool call's arguments to the tool's input schema
// before anything reaches the API: no unknown arguments (a misspelled
// "bran" must not silently drop a brand filter), every required one, and
// each of the type it declares. It checks the top level, which is all the
// tools' schemas constrain beyond what the API checks itself.
func checkArgs(schema map[string]any, args map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	if schema["additionalProperties"] == false {
		for name := range args {
			if _, ok := props[name]; !ok {
				return fmt.Errorf("unknown argument %q; this tool takes %s", name, argList(props))
			}
		}
	}
	required, _ := schema["required"].([]string)
	for _, name := range required {
		if v, ok := args[name]; !ok || v == nil || v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, v := range args {
		p, _ := props[name].(map[string]any)
		if p == nil || v == nil {
			continue
		}
		if err := checkType(name, p, v); err != nil {
			return err
		}
	}
	return nil
}

func checkType(name string, p map[string]any, v any) error {
	want, _ := p["type"].(string)
	ok := true
	switch want {
	case "string":
		s, isString := v.(string)
		ok = isString
		if values, hasEnum := p["enum"].([]string); isString && hasEnum && !slices.Contains(values, s) {
			return fmt.Errorf("%s must be one of %s, not %q", name, strings.Join(values, ", "), s)
		}
	case "integer":
		f, isNumber := v.(float64)
		ok = isNumber && f == math.Trunc(f)
	case "number":
		_, ok = v.(float64)
	case "boolean":
		_, ok = v.(bool)
	case "array":
		items, isArray := v.([]any)
		ok = isArray
		if item, _ := p["items"].(map[string]any); isArray && item != nil {
			for i, it := range items {
				if err := checkType(fmt.Sprintf("%s[%d]", name, i), item, it); err != nil {
					return err
				}
			}
		}
	case "object":
		_, ok = v.(map[string]any)
	}
	if !ok {
		return fmt.Errorf("%s must be %s %s", name, article(want), want)
	}
	return nil
}

func article(t string) string {
	if strings.IndexAny(t[:1], "aeiou") == 0 {
		return "an"
	}
	return "a"
}

func argList(props map[string]any) string {
	if len(props) == 0 {
		return "no arguments"
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
