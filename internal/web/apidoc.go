// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"

	contract "github.com/spectrum-labs-tech/araldo/api"
)

// The API reference page, rendered from the OpenAPI contract (ADR 0005), so
// the page and the contract never disagree.

type apiDoc struct {
	Title       string
	Version     string
	Description template.HTML
	Tags        []apiTag
	Schemas     []apiSchema
}

type apiTag struct {
	Name string
	Ops  []apiOp
}

type apiOp struct {
	Anchor      string
	Method      string
	Path        string
	Summary     string
	Description template.HTML
	Public      bool
	Params      []apiField
	Body        []apiField
	BodyRef     string
	// Form lists the fields of a multipart/form-data body (uploads).
	Form      []apiField
	Responses []apiResponse
}

type apiField struct {
	Name        string
	In          string
	Type        string
	Ref         string // schema anchor the type links to
	Required    bool
	Enum        []string
	Description template.HTML
}

type apiResponse struct {
	Status      string
	Description string
	Ref         string
	Type        string
}

type apiSchema struct {
	Name        string
	Anchor      string
	Description template.HTML
	Type        string
	Enum        []string
	Fields      []apiField
}

// buildAPIDoc reads the embedded contract.
func buildAPIDoc() (*apiDoc, error) {
	doc, err := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if err != nil {
		return nil, err
	}
	// The contract is validated by internal/api's tests; here it is only read.
	d := &apiDoc{Title: doc.Info.Title, Version: doc.Info.Version, Description: markdown(doc.Info.Description)}
	byTag := map[string]*apiTag{}
	var order []string
	for _, t := range doc.Tags {
		byTag[t.Name] = &apiTag{Name: t.Name}
		order = append(order, t.Name)
	}
	paths, err := pathOrder(contract.OpenAPI)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		item := doc.Paths.Value(path)
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			op := item.GetOperation(method)
			if op == nil {
				continue
			}
			o := apiOp{
				Anchor: anchor(method + "-" + path), Method: method, Path: path, Summary: op.Summary,
				Description: markdown(op.Description), Public: op.Security != nil && len(*op.Security) == 0,
			}
			for _, p := range append(item.Parameters, op.Parameters...) {
				if p.Value == nil {
					continue
				}
				f := fieldOf(p.Value.Name, p.Value.Schema, p.Value.Required, p.Value.Description)
				f.In = p.Value.In
				o.Params = append(o.Params, f)
			}
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				if mt := op.RequestBody.Value.Content.Get("application/json"); mt != nil && mt.Schema != nil {
					o.BodyRef = refAnchor(mt.Schema.Ref)
					o.Body = fieldsOf(mt.Schema)
				}
				if mt := op.RequestBody.Value.Content.Get("multipart/form-data"); mt != nil && mt.Schema != nil {
					o.Form = fieldsOf(mt.Schema)
				}
			}
			for _, status := range sortedKeys(op.Responses.Map()) {
				r := op.Responses.Value(status).Value
				if r == nil {
					continue
				}
				resp := apiResponse{Status: status, Description: deref(r.Description)}
				for _, mt := range r.Content {
					if mt.Schema != nil {
						resp.Type, resp.Ref = typeOf(mt.Schema)
					}
				}
				if status == "default" {
					resp.Status = "4xx/5xx"
				}
				o.Responses = append(o.Responses, resp)
			}
			tag := "Other"
			if len(op.Tags) > 0 {
				tag = op.Tags[0]
			}
			if byTag[tag] == nil {
				byTag[tag] = &apiTag{Name: tag}
				order = append(order, tag)
			}
			byTag[tag].Ops = append(byTag[tag].Ops, o)
		}
	}
	for _, name := range order {
		if t := byTag[name]; len(t.Ops) > 0 {
			d.Tags = append(d.Tags, *t)
		}
	}
	for _, name := range sortedKeys(doc.Components.Schemas) {
		ref := doc.Components.Schemas[name]
		s := apiSchema{Name: name, Anchor: "schema-" + anchor(name), Description: markdown(ref.Value.Description)}
		switch {
		case len(ref.Value.Enum) > 0:
			s.Type, s.Enum = "string", enumOf(ref.Value)
		default:
			s.Type = "object"
			s.Fields = fieldsOf(ref)
		}
		d.Schemas = append(d.Schemas, s)
	}
	return d, nil
}

// fieldsOf lists an object schema's properties, merging allOf.
func fieldsOf(ref *openapi3.SchemaRef) []apiField {
	if ref == nil || ref.Value == nil {
		return nil
	}
	s := ref.Value
	var out []apiField
	for _, part := range s.AllOf {
		out = append(out, fieldsOf(part)...)
	}
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	for _, name := range sortedKeys(s.Properties) {
		p := s.Properties[name]
		desc := ""
		if p.Value != nil {
			desc = p.Value.Description
		}
		out = append(out, fieldOf(name, p, required[name], desc))
	}
	return out
}

func fieldOf(name string, ref *openapi3.SchemaRef, required bool, desc string) apiField {
	f := apiField{Name: name, Required: required, Description: markdown(desc)}
	f.Type, f.Ref = typeOf(ref)
	if ref != nil && ref.Value != nil {
		f.Enum = enumOf(ref.Value)
	}
	return f
}

// typeOf names a schema's type, and the schema anchor it links to.
func typeOf(ref *openapi3.SchemaRef) (string, string) {
	if ref == nil {
		return "", ""
	}
	if ref.Ref != "" {
		name := strings.TrimPrefix(ref.Ref, "#/components/schemas/")
		return name, "schema-" + anchor(name)
	}
	s := ref.Value
	if s == nil {
		return "", ""
	}
	if len(s.AllOf) > 0 {
		for _, part := range s.AllOf {
			if part.Ref != "" && !strings.HasSuffix(part.Ref, "/ListBase") {
				return typeOf(part)
			}
		}
		// A list: find the data items (in the part that is not ListBase).
		for _, part := range s.AllOf {
			if part.Ref == "" && part.Value != nil {
				if data := part.Value.Properties["data"]; data != nil && data.Value != nil && data.Value.Items != nil {
					t, r := typeOf(data.Value.Items)
					return "list of " + t, r
				}
			}
		}
		return "object", ""
	}
	switch {
	case s.Type.Is("array"):
		t, r := typeOf(s.Items)
		return "array of " + t, r
	case s.Type.Is("object") && s.AdditionalProperties.Schema != nil:
		t, r := typeOf(s.AdditionalProperties.Schema)
		return "map of " + t, r
	case s.Type != nil && len(s.Type.Slice()) > 0:
		t := s.Type.Slice()[0]
		if s.Format != "" {
			t += " (" + s.Format + ")"
		}
		return t, ""
	}
	return "any", ""
}

// pathOrder lists the contract's paths in the order they are written, so
// the page reads the way the contract does.
func pathOrder(src []byte) ([]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("empty contract")
	}
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "paths" {
			continue
		}
		var out []string
		m := top.Content[i+1]
		for j := 0; j+1 < len(m.Content); j += 2 {
			out = append(out, m.Content[j].Value)
		}
		return out, nil
	}
	return nil, fmt.Errorf("contract has no paths")
}

func enumOf(s *openapi3.Schema) []string {
	var out []string
	for _, e := range s.Enum {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func refAnchor(ref string) string {
	if ref == "" {
		return ""
	}
	return "schema-" + anchor(strings.TrimPrefix(ref, "#/components/schemas/"))
}

var nonAnchor = regexp.MustCompile(`[^a-z0-9]+`)

func anchor(s string) string {
	return strings.Trim(nonAnchor.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var (
	codeSpan = regexp.MustCompile("`([^`]+)`")
	bold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// markdown renders the small subset the contract's descriptions use:
// paragraphs, "- " lists, `code` and **bold**. Input is escaped first.
func markdown(src string) template.HTML {
	var sb strings.Builder
	inline := func(s string) string {
		s = html.EscapeString(s)
		s = codeSpan.ReplaceAllString(s, "<code>$1</code>")
		return bold.ReplaceAllString(s, "<strong>$1</strong>")
	}
	for block := range strings.SplitSeq(strings.TrimSpace(src), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 || lines[0] == "" {
			continue
		}
		if strings.HasPrefix(lines[0], "- ") {
			sb.WriteString("<ul>")
			for _, l := range lines {
				sb.WriteString("<li>" + inline(strings.TrimPrefix(strings.TrimSpace(l), "- ")) + "</li>")
			}
			sb.WriteString("</ul>")
			continue
		}
		sb.WriteString("<p>" + inline(strings.Join(lines, " ")) + "</p>")
	}
	return template.HTML(sb.String()) //nolint:gosec // built from escaped text
}
