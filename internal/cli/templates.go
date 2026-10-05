// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// templateFields are what --json can pick, as GET /v1/templates names them.
var templateFields = []string{"approval", "brand", "created_at", "id", "key", "latest", "latest_version", "name", "updated_at"}

const templatesUsage = "usage: araldo templates list [--brand B] | get KEY or ID [--brand B] [--version N]  " +
	"[--live] [--org ORG] [--hostname H] [--json fields] [--jq expr]"

// runTemplates reads templates through the API: which there are, and one's
// text and the data it takes, to post with araldo posts create --template.
func runTemplates(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "list" && args[0] != "get") {
		return usageErr(templatesUsage)
	}
	cmd, rest := args[0], args[1:]
	var ref string
	if cmd == "get" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		ref, rest = rest[0], rest[1:]
	}
	if bareJSON(rest) {
		return jsonFieldsHelp(templateFields)
	}
	var t target
	var brand, jsonFields, jq string
	var version int
	if err := flags("templates "+cmd, stderr, rest, func(fs *flag.FlagSet) {
		clientFlags(fs, &t)
		fs.StringVar(&brand, "brand", "", "the brand (ID or slug); needed to find a template by key")
		fs.StringVar(&jsonFields, "json", "", "print these fields as JSON (comma-separated): "+joinFields(templateFields))
		fs.StringVar(&jq, "jq", "", "filter the --json output with a jq expression")
		if cmd == "get" {
			fs.IntVar(&version, "version", 0, "an earlier version (default: the latest)")
		}
	}); err != nil {
		return err
	}
	if cmd == "get" && ref == "" {
		return usageErr("name the template: araldo templates get KEY --brand B, or get tmpl_…")
	}
	if cmd == "get" && !strings.HasPrefix(ref, "tmpl_") && brand == "" {
		return usageErr("--brand is needed to find a template by its key")
	}
	out, err := newOutput(stdout, jsonFields, jq, templateFields)
	if err != nil {
		return err
	}
	c, _, err := connect(t)
	if err != nil {
		return err
	}
	q := url.Values{}
	if brand != "" {
		q.Set("brand", brand)
	}
	if cmd == "get" && !strings.HasPrefix(ref, "tmpl_") {
		q.Set("key", ref)
	}
	var tpls list
	if cmd == "list" || !strings.HasPrefix(ref, "tmpl_") {
		raw, err := c.Do(ctx, http.MethodGet, "/v1/templates", q, nil, "")
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &tpls); err != nil {
			return err
		}
	}
	if cmd == "list" {
		if out.fields != nil {
			return out.printJSON(tpls.Data)
		}
		if len(tpls.Data) == 0 {
			out.empty(stderr, "no templates")
			return nil
		}
		rows := make([][]string, 0, len(tpls.Data))
		for _, tp := range tpls.Data {
			v, _ := tp["latest_version"].(float64)
			rows = append(rows, []string{str(tp, "key"), str(tp, "name"), fmt.Sprintf("v%d", int(v)), str(tp, "approval"), str(tp, "id")})
		}
		return out.table([]string{"KEY", "NAME", "VERSION", "APPROVAL", "ID"}, rows)
	}
	if !strings.HasPrefix(ref, "tmpl_") {
		if len(tpls.Data) == 0 {
			return fmt.Errorf("the brand has no template %q", ref)
		}
		ref = str(tpls.Data[0], "id")
	}
	vq := url.Values{}
	if version > 0 {
		vq.Set("version", fmt.Sprint(version))
	}
	raw, err := c.Do(ctx, http.MethodGet, "/v1/templates/"+url.PathEscape(ref), vq, nil, "")
	if err != nil {
		return err
	}
	var tp map[string]any
	if err := json.Unmarshal(raw, &tp); err != nil {
		return err
	}
	if out.fields != nil {
		return out.printJSON([]map[string]any{tp})
	}
	return printTemplate(stdout, tp)
}

// printTemplate writes a template's text and the data it takes for a person.
func printTemplate(w io.Writer, tp map[string]any) error {
	var b strings.Builder
	latest, _ := tp["latest"].(map[string]any)
	v, _ := latest["version"].(float64)
	fmt.Fprintf(&b, "%s  %s  v%d  approval: %s  [%s]\n\n", str(tp, "key"), str(tp, "name"), int(v), str(tp, "approval"), str(tp, "id"))
	fmt.Fprintf(&b, "%s\n", str(latest, "body"))
	if ov, ok := latest["overrides"].(map[string]any); ok {
		for _, p := range slices.Sorted(maps.Keys(ov)) {
			fmt.Fprintf(&b, "\nfor %s:\n%v\n", p, ov[p])
		}
	}
	if vars, ok := latest["variables"]; ok && vars != nil {
		raw, err := json.MarshalIndent(vars, "", "  ")
		if err == nil {
			fmt.Fprintf(&b, "\ndata (JSON Schema):\n%s\n", raw)
		}
	}
	if ex, ok := latest["examples"].([]any); ok && len(ex) > 0 {
		raw, err := json.Marshal(ex[0])
		if err == nil {
			fmt.Fprintf(&b, "\nexample data: %s\n", raw)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
