// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestAPIDocCoversTheContract(t *testing.T) {
	t.Parallel()
	d, err := buildAPIDoc()
	if err != nil {
		t.Fatal(err)
	}
	anchors := map[string]bool{}
	ops := 0
	for _, tag := range d.Tags {
		for _, op := range tag.Ops {
			ops++
			anchors[op.Anchor] = true
		}
	}
	if ops < 30 {
		t.Fatalf("only %d operations rendered", ops)
	}
	for _, s := range d.Schemas {
		anchors[s.Anchor] = true
	}
	// Every type link points at a rendered object.
	check := func(ref, where string) {
		if ref != "" && !anchors[ref] {
			t.Errorf("%s links to #%s, which is not rendered", where, ref)
		}
	}
	for _, tag := range d.Tags {
		for _, op := range tag.Ops {
			check(op.BodyRef, op.Anchor)
			for _, f := range append(op.Params, op.Body...) {
				check(f.Ref, op.Anchor+"/"+f.Name)
			}
			for _, r := range op.Responses {
				check(r.Ref, op.Anchor+"/"+r.Status)
			}
		}
	}
	var post apiOp
	for _, tag := range d.Tags {
		for _, op := range tag.Ops {
			if op.Method == http.MethodPost && op.Path == "/v1/posts" {
				post = op
			}
		}
	}
	if d.Tags[0].Name != "Brands" || d.Tags[0].Ops[0].Path != "/v1/brands" || d.Tags[0].Ops[0].Method != http.MethodGet {
		t.Errorf("first operation %s %s in %s, want the contract's order", d.Tags[0].Ops[0].Method, d.Tags[0].Ops[0].Path, d.Tags[0].Name)
	}
	if r := d.Tags[0].Ops[0].Responses[0]; r.Type != "list of Brand" || r.Ref != "schema-brand" {
		t.Errorf("list response type %q ref %q, want list of Brand", r.Type, r.Ref)
	}
	if post.BodyRef != "schema-postinput" || len(post.Body) == 0 {
		t.Errorf("POST /v1/posts body: ref %q, %d fields", post.BodyRef, len(post.Body))
	}
}

func TestMarkdownEscapes(t *testing.T) {
	t.Parallel()
	got := string(markdown("Use `a<b>` and **bold**.\n\n- one\n- <two>"))
	want := "<p>Use <code>a&lt;b&gt;</code> and <strong>bold</strong>.</p><ul><li>one</li><li>&lt;two&gt;</li></ul>"
	if got != want {
		t.Fatalf("markdown = %q\nwant       %q", got, want)
	}
	if strings.Contains(string(markdown("<script>")), "<script>") {
		t.Fatal("markdown did not escape HTML")
	}
}
