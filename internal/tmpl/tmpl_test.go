// SPDX-License-Identifier: AGPL-3.0-or-later

package tmpl

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

const buildSchema = `{
  "type": "object",
  "required": ["name", "url"],
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "url": {"type": "string"},
    "parts": {"type": "integer", "minimum": 0},
    "tags": {"type": "array", "items": {"type": "string"}}
  }
}`

func featured(t *testing.T) *Compiled {
	t.Helper()
	c, err := Compile(Source{
		Variables: json.RawMessage(buildSchema),
		Examples:  []json.RawMessage{json.RawMessage(`{"name":"Atlas","url":"https://araldo.dev/b/1","parts":12,"tags":["Wi-Fi 6","launch day"]}`)},
		Body:      `Featured build: {{.name}} ({{.parts}} {{plural .parts "part" "parts"}}) {{.url}} {{hashtags .tags}}`,
		Overrides: map[platform.Provider]string{platform.LinkedIn: `This week's featured build is {{.name}}. {{.url}}`},
		Fit:       map[platform.Provider]platform.Fit{platform.X: platform.FitTruncate},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRender(t *testing.T) {
	t.Parallel()
	c := featured(t)
	data := c.Examples()[0]
	got, err := c.Render(platform.Bluesky, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "Featured build: Atlas (12 parts) https://araldo.dev/b/1 #WiFi6 #launchDay"
	if got != want {
		t.Fatalf("Render = %q\nwant     %q", got, want)
	}
	li, err := c.Render(platform.LinkedIn, data, nil)
	if err != nil || !strings.HasPrefix(li, "This week's featured build is Atlas.") {
		t.Fatalf("override = %q, %v", li, err)
	}
	if c.FitFor(platform.X) != platform.FitTruncate || c.FitFor(platform.Bluesky) != platform.FitError {
		t.Fatal("FitFor")
	}
}

func problemCodes(t *testing.T, err error) []string {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("error %v is not an *apperr.Error", err)
	}
	var codes []string
	for _, p := range ae.Problems {
		codes = append(codes, p.Code+"@"+p.Param)
	}
	return codes
}

func TestValidate(t *testing.T) {
	t.Parallel()
	c := featured(t)
	if err := c.Validate(json.RawMessage(`{"name":"x","url":"u"}`)); err != nil {
		t.Fatalf("valid data: %v", err)
	}
	tests := []struct {
		data string
		want string
	}{
		{`{"url":"u"}`, "data_invalid@data"},
		{`{"name":"","url":"u"}`, "data_invalid@data.name"},
		{`{"name":"x","url":"u","parts":-1}`, "data_invalid@data.parts"},
		{`[1,2]`, "data_invalid@data"},
		{`not json`, "data_invalid@data"},
	}
	for _, tt := range tests {
		codes := problemCodes(t, c.Validate(json.RawMessage(tt.data)))
		if !strings.Contains(strings.Join(codes, ","), tt.want) {
			t.Errorf("Validate(%s) problems %v, want %s", tt.data, codes, tt.want)
		}
	}
	big := `{"name":"` + strings.Repeat("a", MaxDataBytes) + `","url":"u"}`
	if codes := problemCodes(t, c.Validate(json.RawMessage(big))); codes[0] != "data_too_large@data" {
		t.Errorf("oversized data: %v", codes)
	}
}

func TestCompileProblems(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  Source
		want string
	}{
		{"no body", Source{}, "body_missing@body"},
		{"syntax", Source{Body: "{{.name"}, "template_syntax@body"},
		{"unknown func", Source{Body: "{{exec .x}}"}, "template_syntax@body"},
		{"template call", Source{Body: `{{define "x"}}hi{{end}}{{template "x"}}`}, "template_syntax@body"},
		{"unknown provider", Source{Body: "x", Overrides: map[platform.Provider]string{"myspace": "y"}}, "provider_unknown@overrides.myspace"},
		{"bad fit", Source{Body: "x", Fit: map[platform.Provider]platform.Fit{platform.X: "squeeze"}}, "fit_invalid@fit.x"},
		{"bad schema", Source{Body: "x", Variables: json.RawMessage(`{"type":"nope"}`)}, "variables_invalid@variables"},
		{"remote ref", Source{Body: "x", Variables: json.RawMessage(`{"$ref":"https://evil.test/s.json"}`)}, "variables_invalid@variables"},
		{"bad example", Source{Body: "x", Variables: json.RawMessage(buildSchema), Examples: []json.RawMessage{json.RawMessage(`{}`)}}, "example_invalid@examples[0]"},
		{"too large", Source{Body: strings.Repeat("a", MaxSourceBytes+1)}, "body_too_large@body"},
	}
	for _, tt := range tests {
		_, err := Compile(tt.src)
		if codes := problemCodes(t, err); !strings.Contains(strings.Join(codes, ","), tt.want) {
			t.Errorf("%s: problems %v, want %s", tt.name, codes, tt.want)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	c, err := Compile(Source{Body: "Hi {{.name}}"})
	if err != nil {
		t.Fatal(err)
	}
	if codes := problemCodes(t, renderErr(c, `{}`)); codes[0] != "variable_missing@data.name" {
		t.Fatalf("missing variable: %v", codes)
	}
	loop, err := Compile(Source{Body: `{{range .items}}{{.}}{{end}}`})
	if err != nil {
		t.Fatal(err)
	}
	items := `{"items":["` + strings.Repeat("x", 1000) + strings.Repeat(`","`+strings.Repeat("x", 1000), 70) + `"]}`
	if codes := problemCodes(t, renderErr(loop, items)); codes[0] != "output_too_large@body" {
		t.Fatalf("huge output: %v", codes)
	}
}

func renderErr(c *Compiled, data string) error {
	_, err := c.Render(platform.X, json.RawMessage(data), nil)
	return err
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	tests := []struct {
		body, data, want string
	}{
		{`{{truncate 10 .s}}`, `{"s":"the quick brown fox"}`, "the quick…"},
		{`{{truncate 50 .s}}`, `{"s":"short"}`, "short"},
		{`{{words 2 .s}}`, `{"s":"one two three"}`, "one two…"},
		{`{{join ", " .l}}`, `{"l":["a","b"]}`, "a, b"},
		{`{{hashtag .s}}`, `{"s":"Wi-Fi 6 router"}`, "#WiFi6Router"},
		{`{{default "n/a" .s}}`, `{"s":""}`, "n/a"},
		{`{{title .s}}`, `{"s":"hello world"}`, "Hello World"},
		{`{{upper .s}}`, `{"s":"x"}`, "X"},
		{`{{date "date" .t}}`, `{"t":"2026-10-01T02:00:00Z"}`, "Sep 30, 2026"},
		{`{{date "15:04" .t}}`, `{"t":"2026-10-01T02:00:00Z"}`, "22:00"},
		{`a{{thread}}b`, `{}`, "a" + platform.ThreadBreak + "b"},
		{`{{urlquery .s}}`, `{"s":"a b&c"}`, "a+b%26c"},
	}
	for _, tt := range tests {
		c, err := Compile(Source{Body: tt.body})
		if err != nil {
			t.Fatalf("%s: %v", tt.body, err)
		}
		got, err := c.Render(platform.X, json.RawMessage(tt.data), ny)
		if err != nil || got != tt.want {
			t.Errorf("%s with %s = %q, %v; want %q", tt.body, tt.data, got, err, tt.want)
		}
	}
}

func TestOptionalFields(t *testing.T) {
	t.Parallel()
	vars := json.RawMessage(`{"type":"object","required":["name"],"properties":{
		"name":{"type":"string"},"tagline":{"type":"string"},
		"price":{"type":"object","properties":{"amount":{"type":"string"},"sale":{"type":"string"}}}}}`)
	tests := []struct {
		name, body, data, want, code string
	}{
		{"guarded optional, absent", "{{.name}}{{if .tagline}}: {{.tagline}}{{end}}", `{"name":"Atlas"}`, "Atlas", ""},
		{"guarded optional, present", "{{.name}}{{if .tagline}}: {{.tagline}}{{end}}", `{"name":"Atlas","tagline":"light"}`, "Atlas: light", ""},
		{"with on a nested optional", "{{.name}}{{with .price}}{{with .sale}} now {{.}}{{end}}{{end}}", `{"name":"Atlas","price":{"amount":"$900"}}`, "Atlas", ""},
		{"unguarded optional, absent", "{{.name}} {{.tagline}}", `{"name":"Atlas"}`, "", "variable_unguarded"},
		{"undeclared field", "{{.name}} {{.color}}", `{"name":"Atlas"}`, "", "variable_missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := Compile(Source{Variables: vars, Body: tt.body})
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Render(platform.Bluesky, json.RawMessage(tt.data), nil)
			if code := apperr.As(err).Code; tt.code != "" && code != tt.code || tt.code == "" && (err != nil || got != tt.want) {
				t.Fatalf("Render = %q, %v; want %q %s", got, err, tt.want, tt.code)
			}
		})
	}
}

func TestLoopsAreBounded(t *testing.T) {
	t.Parallel()
	// Data within MaxDataBytes: 30,000 short items.
	many := `{"items":[` + strings.TrimSuffix(strings.Repeat(`0,`, 30000), ",") + `],"tags":["a","b"]}`
	tests := []struct {
		name    string
		body    string
		data    string
		compile string // the problem Compile reports, or ""
		render  string // the problem Render reports, or ""
	}{
		{"a range over a number", `{{range 2000000000}}{{end}}`, `{}`, "template_syntax@body", ""},
		{"a range over a number in a pipeline", `{{range $i := 2000000000}}{{end}}`, `{}`, "template_syntax@body", ""},
		{"ranges nested too deep", `{{range .a}}{{range .b}}{{range .c}}{{range .d}}{{end}}{{end}}{{end}}{{end}}`, `{}`, "template_syntax@body", ""},
		{"a printf too wide", `{{printf "%0999999999d" 1}}`, `{}`, "template_syntax@body", ""},
		{"a printf width from an argument", `{{printf "%0*d" 999999999 1}}`, `{}`, "template_syntax@body", ""},
		{"a printf format from the data", `{{printf .f 1}}`, `{"f":"%d"}`, "template_syntax@body", ""},
		{"nested loops that print nothing", `{{range .items}}{{range $.items}}{{end}}{{end}}`, many, "", "loop_limit@body"},
		{"a range over a computed number", `{{range len .s}}{{range len $.s}}{{end}}{{end}}`, `{"s":"` + strings.Repeat("x", 200) + `"}`, "", "loop_limit@body"},
		{"ordinary loops", `{{range .tags}}{{range $.tags}}{{.}}{{end}}{{end}} {{printf "%05.1f" 3.14159}} {{printf "%d%%" 5}}`, many, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := Compile(Source{Body: tt.body})
			if tt.compile != "" {
				if codes := problemCodes(t, err); !strings.Contains(strings.Join(codes, ","), tt.compile) {
					t.Fatalf("Compile problems %v, want %s", codes, tt.compile)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = renderErr(c, tt.data)
			if tt.render == "" {
				if err != nil {
					t.Fatalf("Render = %v", err)
				}
				return
			}
			if codes := problemCodes(t, err); len(codes) == 0 || codes[0] != tt.render {
				t.Fatalf("Render problems %v, want %s", codes, tt.render)
			}
		})
	}
}

func TestLoopCountIsPerRender(t *testing.T) {
	t.Parallel()
	c, err := Compile(Source{Body: `{{range .items}}{{.}}{{end}}`})
	if err != nil {
		t.Fatal(err)
	}
	data := `{"items":[` + strings.TrimSuffix(strings.Repeat(`1,`, MaxIterations), ",") + `]}`
	// Each render may use the whole allowance.
	for range 3 {
		if err := renderErr(c, data); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderIssue(t *testing.T) {
	t.Parallel()
	c, err := Compile(Source{
		Subject:     "{{len .offers}} {{plural (len .offers) \"deal\" \"deals\"}} this week",
		PreviewText: "Starting with\n  {{(index .offers 0).title}}",
		Body:        "# Deals\n{{range .offers}}\n- [{{md .title}}]({{.url}}){{end}}",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.RenderIssue(json.RawMessage(`{"offers":[{"title":"*Big* sale","url":"https://a.example"},{"title":"#2","url":"https://b.example"}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Issue{Subject: "2 deals this week", PreviewText: "Starting with *Big* sale",
		Body: "# Deals\n\n- [" + `\*Big\* sale` + "](https://a.example)\n- [" + `\#2` + "](https://b.example)"}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	// A problem in the subject is reported against the subject.
	c, err = Compile(Source{Subject: "{{.missing}}", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.RenderIssue(json.RawMessage(`{}`), nil)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != "variable_missing" {
		t.Fatalf("missing field in the subject: %v", err)
	}
	if _, err := Compile(Source{Subject: "{{if}}", Body: "x"}); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Errorf("a subject that does not parse: %v", err)
	}
	c, _ = Compile(Source{Subject: "{{.s}}", Body: "x"})
	if _, err := c.RenderIssue(json.RawMessage(`{"s": 1}`), nil); err != nil {
		t.Errorf("a number in the subject: %v", err)
	}
	if _, err = Compile(Source{Subject: "{{.s | printf \"%*d\" 9}}", Body: "x"}); err == nil {
		t.Error("the subject is held to the body's rules")
	}
}
