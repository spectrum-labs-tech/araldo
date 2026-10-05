// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"fmt"
	"strings"
	"testing"
)

// TestPostsCommands checks araldo posts through the API: what it sends,
// and what a person or a script reads back. It sets environment variables
// (cliEnv), so it cannot run in parallel.
func TestPostsCommands(t *testing.T) {
	srv, last := fakeAraldo(t)
	cliEnv(t, srv, true)

	code, out, errOut := runCLI(t, "", "posts", "list", "--status", "scheduled", "--limit", "2")
	if code != ExitOK || !strings.Contains(out, "post_1") || !strings.Contains(out, "post_2") || strings.Contains(out, "post_3") {
		t.Fatalf("list: exit %d %q %q", code, out, errOut)
	}

	code, out, errOut = runCLI(t, "", "posts", "preview", "--brand", "araldo", "--body", "Shipped 1.0")
	if code != ExitOK || !strings.Contains(out, "[11/300] Shipped 1.0") {
		t.Fatalf("preview: exit %d %q %q", code, out, errOut)
	}
	if c, _ := (*last)["content"].(map[string]any); c["body"] != "Shipped 1.0" || (*last)["brand"] != "araldo" {
		t.Fatalf("preview body %v", *last)
	}
	code, out, errOut = runCLI(t, "", "posts", "preview", "--brand", "araldo", "--body", "this is too long")
	if code == ExitOK || !strings.Contains(out, "too_long: Bluesky posts are at most 300 characters.") || !strings.Contains(errOut, "does not fit") {
		t.Fatalf("a preview that does not fit: exit %d %q %q", code, out, errOut)
	}

	code, out, errOut = runCLI(t, "", "posts", "create", "--brand", "araldo", "--template", "release", "--data", `{"version":"1.0"}`,
		"--at", "now", "--media", "media_1, media_2", "--idempotency-key", "release-1.0")
	if code != ExitOK || out != "post_9\n" || !strings.Contains(errOut, "Created post_9: scheduled") {
		t.Fatalf("create: exit %d %q %q", code, out, errOut)
	}
	if b := *last; b["template"] != "release" || b["publish_at"] != "now" || fmt.Sprint(b["media"]) != "[media_1 media_2]" ||
		fmt.Sprint(b["data"]) != "map[version:1.0]" || b["content"] != nil {
		t.Fatalf("create body %v", b)
	}
	code, out, _ = runCLI(t, "Line one\nLine two\n", "posts", "create", "--brand", "araldo", "--body-file", "-")
	if c, _ := (*last)["content"].(map[string]any); code != ExitOK || c["body"] != "Line one\nLine two" || (*last)["publish_at"] != "next_slot" {
		t.Fatalf("create from stdin: exit %d %q %v", code, out, *last)
	}
	for _, bad := range [][]string{
		{"posts", "create", "--body", "x"},
		{"posts", "create", "--brand", "araldo"},
		{"posts", "create", "--brand", "araldo", "--body", "x", "--template", "release"},
		{"posts", "create", "--brand", "araldo", "--template", "release", "--data", "{nope"},
		{"posts", "get"},
		{"posts", "publish"},
	} {
		if code, _, errOut := runCLI(t, "", bad...); code != ExitUsage {
			t.Errorf("%v: exit %d %q", bad, code, errOut)
		}
	}

	code, out, errOut = runCLI(t, "", "posts", "get", "post_9")
	for _, want := range []string{"post_9  needs attention", "Shipped 1.0",
		"araldo.dev (bluesky): published  https://bsky.app/p/1  [ptgt_1]", "chan_2 (x): needs attention  timed out  [ptgt_2]"} {
		if code != ExitOK || !strings.Contains(out, want) {
			t.Errorf("get lacks %q: exit %d\n%s%s", want, code, out, errOut)
		}
	}
	if code, out, _ = runCLI(t, "", "posts", "get", "post_9", "--json", "status", "--jq", ".[0].status"); code != ExitOK || out != "needs_attention\n" {
		t.Fatalf("get --json: exit %d %q", code, out)
	}
	if code, _, errOut = runCLI(t, "", "posts", "cancel", "post_9"); code != ExitOK || !strings.Contains(errOut, "post_9 is canceled") {
		t.Fatalf("cancel: exit %d %q", code, errOut)
	}
}
