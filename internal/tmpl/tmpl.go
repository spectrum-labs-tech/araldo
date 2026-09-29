// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tmpl compiles and renders post templates (ADR 0010): Go
// text/templates with a fixed set of helpers, one default body and
// optional per-platform bodies, and a JSON Schema for the data callers
// send.
package tmpl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Limits (ADR 0010).
const (
	MaxSourceBytes = 16 << 10
	MaxDataBytes   = 64 << 10
	MaxOutputBytes = 64 << 10
)

// Source is a template version as authored.
type Source struct {
	// Variables is a JSON Schema for the data. Empty means any object.
	Variables json.RawMessage
	// Examples are sample data, used to check the bodies and for previews.
	Examples []json.RawMessage
	Body     string
	// Overrides replace Body on a platform.
	Overrides map[platform.Provider]string
	// Fit says what to do with text that is too long, per platform.
	Fit map[platform.Provider]platform.Fit
}

// Compiled is a template ready to render.
type Compiled struct {
	src       Source
	schema    *jsonschema.Schema
	body      *template.Template
	overrides map[platform.Provider]*template.Template
}

// Compile checks and prepares src. Problems are returned together as an
// *apperr.Error.
func Compile(src Source) (*Compiled, error) {
	var ps apperr.Problems
	c := &Compiled{src: src, overrides: map[platform.Provider]*template.Template{}}
	if strings.TrimSpace(src.Body) == "" {
		ps.Add("body_missing", "body", "A template needs a body.")
	}
	c.body = parseBody("body", src.Body, &ps)
	for p, body := range src.Overrides {
		if _, ok := platform.RulesFor(p); !ok {
			ps.Add("provider_unknown", "overrides."+string(p), "Unknown platform %q.", p)
			continue
		}
		if t := parseBody("overrides."+string(p), body, &ps); t != nil {
			c.overrides[p] = t
		}
	}
	for p, f := range src.Fit {
		if _, ok := platform.RulesFor(p); !ok {
			ps.Add("provider_unknown", "fit."+string(p), "Unknown platform %q.", p)
		} else if !f.Valid() {
			ps.Add("fit_invalid", "fit."+string(p), "Fit must be error, truncate or thread.")
		}
	}
	if len(bytes.TrimSpace(src.Variables)) > 0 {
		sch, err := compileSchema(src.Variables)
		if err != nil {
			ps.Add("variables_invalid", "variables", "The variables schema is not valid JSON Schema: %v", err)
		}
		c.schema = sch
	}
	if err := ps.Err("The template is not valid."); err != nil {
		return nil, err
	}
	for i, ex := range src.Examples {
		if err := c.Validate(ex); err != nil {
			ps.Add("example_invalid", fmt.Sprintf("examples[%d]", i), "Example %d does not match the variables schema: %v", i+1, err)
		}
	}
	return c, ps.Err("The template is not valid.")
}

func parseBody(name, body string, ps *apperr.Problems) *template.Template {
	if len(body) > MaxSourceBytes {
		ps.Add("body_too_large", name, "Template bodies are limited to %d bytes.", MaxSourceBytes)
		return nil
	}
	t, err := template.New(name).Option("missingkey=error").Funcs(funcs).Parse(body)
	if err != nil {
		ps.Add("template_syntax", name, "%s", strings.TrimPrefix(err.Error(), "template: "))
		return nil
	}
	if msg := forbidden(t.Root); msg != "" {
		ps.Add("template_syntax", name, "%s", msg)
		return nil
	}
	return t
}

// forbidden refuses {{define}}, {{template}} and {{block}}: bodies are
// single templates, which keeps rendering bounded.
func forbidden(n parse.Node) string {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return ""
		}
		for _, c := range n.Nodes {
			if m := forbidden(c); m != "" {
				return m
			}
		}
	case *parse.TemplateNode:
		return "{{template}} and {{block}} are not allowed in bodies"
	case *parse.IfNode:
		return first(forbidden(n.List), forbidden(n.ElseList))
	case *parse.RangeNode:
		return first(forbidden(n.List), forbidden(n.ElseList))
	case *parse.WithNode:
		return first(forbidden(n.List), forbidden(n.ElseList))
	}
	return ""
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type noLoader struct{}

func (noLoader) Load(u string) (any, error) {
	return nil, fmt.Errorf("external schema references are not allowed (%s)", u)
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noLoader{})
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("araldo:variables.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("araldo:variables.json")
}

// Validate checks data against the variables schema. Problems name the
// offending field as data.<path>.
func (c *Compiled) Validate(data json.RawMessage) error {
	if len(data) > MaxDataBytes {
		return apperr.Invalid("data_too_large", "data", "Template data is limited to %d bytes.", MaxDataBytes)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(orEmptyObject(data)))
	if err != nil {
		return apperr.Invalid("data_invalid", "data", "Template data is not valid JSON: %v", err)
	}
	if _, ok := inst.(map[string]any); !ok {
		return apperr.Invalid("data_invalid", "data", "Template data must be a JSON object.")
	}
	if c.schema == nil {
		return nil
	}
	err = c.schema.Validate(inst)
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	var ps apperr.Problems
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil {
			continue
		}
		param := "data" + strings.ReplaceAll(u.InstanceLocation, "/", ".")
		ps.Add("data_invalid", param, "%s", u.Error.String())
	}
	if len(ps) == 0 {
		ps.Add("data_invalid", "data", "%s", ve.Error())
	}
	return ps.Err("Template data does not match the template's variables.")
}

func orEmptyObject(b json.RawMessage) []byte {
	if len(bytes.TrimSpace(b)) == 0 {
		return []byte("{}")
	}
	return b
}

// Examples returns the template's sample data.
func (c *Compiled) Examples() []json.RawMessage { return c.src.Examples }

// FitFor is the fit mode for a platform (error by default).
func (c *Compiled) FitFor(p platform.Provider) platform.Fit {
	if f, ok := c.src.Fit[p]; ok {
		return f
	}
	return platform.FitError
}

// Render renders the body for platform p (its override, or the default)
// with data, formatting dates in loc.
func (c *Compiled) Render(p platform.Provider, data json.RawMessage, loc *time.Location) (string, error) {
	t := c.body
	if o, ok := c.overrides[p]; ok {
		t = o
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(orEmptyObject(data)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return "", apperr.Invalid("data_invalid", "data", "Template data must be a JSON object.")
	}
	if loc == nil {
		loc = time.UTC
	}
	t, err := t.Clone()
	if err != nil {
		return "", err
	}
	t.Funcs(template.FuncMap{"date": dateIn(loc)})
	var out limitedBuffer
	if err := t.Execute(&out, m); err != nil {
		return "", renderError(err)
	}
	return strings.TrimSpace(out.String()), nil
}

var missingKeyRE = regexp.MustCompile(`map has no entry for key "([^"]+)"`)

func renderError(err error) error {
	if errors.Is(err, errOutputTooLarge) {
		return apperr.Invalid("output_too_large", "body", "The rendered text is over %d bytes.", MaxOutputBytes)
	}
	msg := strings.TrimPrefix(err.Error(), "template: ")
	if m := missingKeyRE.FindStringSubmatch(msg); m != nil {
		return apperr.Invalid("variable_missing", "data."+m[1], "The template uses %q, but the data has no such field.", m[1])
	}
	return apperr.Invalid("render_failed", "body", "%s", msg)
}

var errOutputTooLarge = errors.New("output too large")

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxOutputBytes {
		return 0, errOutputTooLarge
	}
	return b.Buffer.Write(p)
}

var _ io.Writer = (*limitedBuffer)(nil)

// funcs are the helpers available in bodies. "date" is replaced per render
// with the brand's time zone.
var funcs = template.FuncMap{
	"truncate": truncate,
	"words":    words,
	"join":     join,
	"hashtags": hashtags,
	"hashtag":  hashtag,
	"lower":    strings.ToLower,
	"upper":    strings.ToUpper,
	"title":    title,
	"trim":     strings.TrimSpace,
	"default":  dflt,
	"plural":   plural,
	"urlquery": url.QueryEscape,
	"thread":   func() string { return platform.ThreadBreak },
	"date":     dateIn(time.UTC),
}

func str(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// truncate shortens s to n characters at a word boundary, adding "…".
func truncate(n int, v any) string {
	s := str(v)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	cut := string(r[:n-1])
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// words keeps the first n words.
func words(n int, v any) string {
	f := strings.Fields(str(v))
	if len(f) <= n {
		return strings.Join(f, " ")
	}
	return strings.Join(f[:n], " ") + "…"
}

func list(v any) []string {
	switch v := v.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, str(x))
		}
		return out
	case []string:
		return v
	case nil:
		return nil
	default:
		return []string{str(v)}
	}
}

func join(sep string, v any) string { return strings.Join(list(v), sep) }

// hashtag turns a phrase into one tag: "AR-15 builds" → "#AR15Builds".
func hashtag(v any) string {
	var sb strings.Builder
	upperNext := false
	for _, w := range strings.FieldsFunc(str(v), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' }) {
		for i, r := range w {
			if i == 0 && upperNext {
				r = unicode.ToUpper(r)
			}
			sb.WriteRune(r)
		}
		upperNext = true
	}
	if sb.Len() == 0 {
		return ""
	}
	return "#" + sb.String()
}

func hashtags(v any) string {
	var out []string
	for _, s := range list(v) {
		if h := hashtag(s); h != "" {
			out = append(out, h)
		}
	}
	return strings.Join(out, " ")
}

func title(v any) string {
	f := strings.Fields(str(v))
	for i, w := range f {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		f[i] = string(r)
	}
	return strings.Join(f, " ")
}

func dflt(d, v any) any {
	if s := str(v); s == "" || s == "0" || s == "false" {
		return d
	}
	return v
}

func plural(n any, one, many string) string {
	if str(n) == "1" {
		return one
	}
	return many
}

// dateIn formats an RFC 3339 time with a Go layout ("Jan 2, 2006") or one
// of the names date, time, datetime.
func dateIn(loc *time.Location) func(string, any) (string, error) {
	return func(layout string, v any) (string, error) {
		t, err := time.Parse(time.RFC3339, str(v))
		if err != nil {
			return "", fmt.Errorf("date: %q is not an RFC 3339 time", str(v))
		}
		switch layout {
		case "date":
			layout = "Jan 2, 2006"
		case "time":
			layout = "3:04 PM"
		case "datetime":
			layout = "Jan 2, 2006 3:04 PM MST"
		}
		return t.In(loc).Format(layout), nil
	}
}
