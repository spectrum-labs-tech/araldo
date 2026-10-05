// SPDX-License-Identifier: AGPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apiclient"
)

// postFields are what --json can pick, as GET /v1/posts names them.
var postFields = []string{"approval", "brand", "content", "created_at", "data", "id", "livemode", "media", "metadata", "publish_at",
	"publish_by", "slot", "status", "targets", "template", "template_version", "updated_at"}

const postsUsage = "usage: araldo posts list [--brand B] [--status S] [--search Q] [--limit N] | get POST | " +
	"preview --brand B (--body TEXT | --template KEY [--data JSON]) | create … [--at WHEN] | cancel POST  " +
	"[--live] [--org ORG] [--hostname H] [--json fields] [--jq expr]"

// runPosts works with posts through the API: list, inspect, check, schedule
// and cancel them, as a developer does while integrating.
func runPosts(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageErr(postsUsage)
	}
	cmd, rest := args[0], args[1:]
	// The post, for get and cancel, comes first, as in gh pr view 12.
	var postID string
	if (cmd == "get" || cmd == "cancel") && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		postID, rest = rest[0], rest[1:]
	}
	var t target
	var brand, status, search, jsonFields, jq, body, bodyFile, template, data, at, by, media, idemKey string
	var limit int
	compose := func(fs *flag.FlagSet) {
		fs.StringVar(&brand, "brand", "", "the brand, by ID or slug (required)")
		fs.StringVar(&body, "body", "", "the text, for every channel (or --template)")
		fs.StringVar(&bodyFile, "body-file", "", "read the text from this file, or - for stdin")
		fs.StringVar(&template, "template", "", "a template's key (or key@version), rendered with --data")
		fs.StringVar(&data, "data", "", "the template's data, as JSON")
		fs.StringVar(&media, "media", "", "media IDs to attach, comma-separated")
	}
	define := func(fs *flag.FlagSet) {
		clientFlags(fs, &t)
		switch cmd {
		case "list":
			fs.StringVar(&brand, "brand", "", "only this brand's posts (ID or slug)")
			fs.StringVar(&status, "status", "", "only posts with this status, such as scheduled, failed or needs_attention")
			fs.StringVar(&search, "search", "", "only posts whose text contains this")
			fs.IntVar(&limit, "limit", 20, "how many, newest first (1 to 100)")
			fs.StringVar(&jsonFields, "json", "", "print these fields as JSON (comma-separated): "+joinFields(postFields))
			fs.StringVar(&jq, "jq", "", "filter the --json output with a jq expression")
		case "get":
			fs.StringVar(&jsonFields, "json", "", "print these fields as JSON (comma-separated): "+joinFields(postFields))
			fs.StringVar(&jq, "jq", "", "filter the --json output with a jq expression")
		case "preview":
			compose(fs)
		case "create":
			compose(fs)
			fs.StringVar(&at, "at", "next_slot", `when: "now", "next_slot" (the brand's next free slot), or an RFC 3339 time`)
			fs.StringVar(&by, "by", "", "an RFC 3339 time to give up by (default: a day after --at)")
			fs.StringVar(&idemKey, "idempotency-key", "", "makes retrying this command safe: the same key never posts twice")
		}
	}
	switch cmd {
	case "list", "get", "preview", "create", "cancel":
	default:
		return usageErr(postsUsage)
	}
	if (cmd == "list" || cmd == "get") && bareJSON(rest) {
		return jsonFieldsHelp(postFields)
	}
	if err := flags("posts "+cmd, stderr, rest, define); err != nil {
		return err
	}
	if (cmd == "get" || cmd == "cancel") && postID == "" {
		return usageErr("name the post: araldo posts %s post_…", cmd)
	}
	var post map[string]any
	if cmd == "preview" || cmd == "create" {
		var err error
		if post, err = postBody(brand, body, bodyFile, template, data, media); err != nil {
			return err
		}
	}
	c, _, err := connect(t)
	if err != nil {
		return err
	}
	switch cmd {
	case "list":
		if limit < 1 || limit > 100 {
			return usageErr("--limit is 1 to 100")
		}
		out, err := newOutput(stdout, jsonFields, jq, postFields)
		if err != nil {
			return err
		}
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		for k, v := range map[string]string{"brand": brand, "status": status, "q": search} {
			if v != "" {
				q.Set(k, v)
			}
		}
		raw, err := c.Do(ctx, http.MethodGet, "/v1/posts", q, nil, "")
		if err != nil {
			return err
		}
		var posts list
		if err := json.Unmarshal(raw, &posts); err != nil {
			return err
		}
		if out.fields != nil {
			return out.printJSON(posts.Data)
		}
		if len(posts.Data) == 0 {
			out.empty(stderr, "no posts")
			return nil
		}
		rows := make([][]string, 0, len(posts.Data))
		for _, p := range posts.Data {
			rows = append(rows, []string{str(p, "status"), when(p), postText(p, 50), targetSummary(p), str(p, "id")})
		}
		return out.table([]string{"STATUS", "WHEN", "TEXT", "CHANNELS", "ID"}, rows)
	case "get":
		out, err := newOutput(stdout, jsonFields, jq, postFields)
		if err != nil {
			return err
		}
		p, err := getPost(ctx, c, postID)
		if err != nil {
			return err
		}
		if out.fields != nil {
			return out.printJSON([]map[string]any{p})
		}
		return printPost(stdout, p)
	case "preview":
		raw, err := c.Do(ctx, http.MethodPost, "/v1/posts/preview", nil, post, "")
		if err != nil {
			return err
		}
		return printPreview(stdout, raw)
	case "create":
		post["publish_at"] = at
		if by != "" {
			post["publish_by"] = by
		}
		raw, err := c.Do(ctx, http.MethodPost, "/v1/posts", nil, post, idemKey)
		if err != nil {
			return err
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stderr, "Created %s: %s, %s.\n", str(p, "id"), strings.ReplaceAll(str(p, "status"), "_", " "), when(p))
		_, _ = fmt.Fprintln(stdout, str(p, "id"))
		return nil
	}
	raw, err := c.Do(ctx, http.MethodPost, "/v1/posts/"+url.PathEscape(postID)+"/cancel", nil, map[string]any{}, "")
	if err != nil {
		return err
	}
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "%s is %s. What had already published stays published.\n", postID, strings.ReplaceAll(str(p, "status"), "_", " "))
	return nil
}

// postBody is a post's text or template, for preview and create.
func postBody(brand, body, bodyFile, template, data, media string) (map[string]any, error) {
	if brand == "" {
		return nil, usageErr("--brand is required")
	}
	if bodyFile != "" {
		var raw []byte
		var err error
		if bodyFile == "-" {
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(bodyFile) //nolint:gosec // G304: the person names the file
		}
		if err != nil {
			return nil, err
		}
		body = strings.TrimRight(string(raw), "\n")
	}
	if (body == "") == (template == "") {
		return nil, usageErr("give the text (--body or --body-file) or a --template, not both")
	}
	p := map[string]any{"brand": brand}
	if template != "" {
		p["template"] = template
		if data != "" {
			if !json.Valid([]byte(data)) {
				return nil, usageErr("--data is not valid JSON")
			}
			p["data"] = json.RawMessage(data)
		}
	} else {
		if data != "" {
			return nil, usageErr("--data goes with --template")
		}
		p["content"] = map[string]any{"body": body}
	}
	if media != "" {
		var ids []string
		for m := range strings.SplitSeq(media, ",") {
			if m = strings.TrimSpace(m); m != "" {
				ids = append(ids, m)
			}
		}
		p["media"] = ids
	}
	return p, nil
}

func getPost(ctx context.Context, c *apiclient.Client, id string) (map[string]any, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/v1/posts/"+url.PathEscape(id), nil, nil, "")
	if err != nil {
		return nil, err
	}
	var p map[string]any
	return p, json.Unmarshal(raw, &p)
}

// when is a post's time, or what it waits for.
func when(p map[string]any) string {
	if at, err := time.Parse(time.RFC3339, str(p, "publish_at")); err == nil {
		return at.Local().Format("2006-01-02 15:04")
	}
	if slot, _ := p["slot"].(bool); slot {
		return "next free slot"
	}
	return "unscheduled"
}

// postText is a post's text, or its template, shortened to n characters.
func postText(p map[string]any, n int) string {
	text := ""
	if c, ok := p["content"].(map[string]any); ok {
		text = str(c, "body")
	}
	if text == "" {
		if t := str(p, "template"); t != "" {
			text = "template " + t
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > n {
		text = string(r[:n-1]) + "…"
	}
	return text
}

// targetSummary counts a post's channels by status: "2 published, 1 failed".
func targetSummary(p map[string]any) string {
	ts, _ := p["targets"].([]any)
	counts := map[string]int{}
	var order []string
	for _, raw := range ts {
		t, _ := raw.(map[string]any)
		s := strings.ReplaceAll(str(t, "status"), "_", " ")
		if counts[s] == 0 {
			order = append(order, s)
		}
		counts[s]++
	}
	parts := make([]string, 0, len(order))
	for _, s := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
	}
	return strings.Join(parts, ", ")
}

// printPost writes a post and each channel's copy for a person.
func printPost(w io.Writer, p map[string]any) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  %s\n", str(p, "id"), strings.ReplaceAll(str(p, "status"), "_", " "), when(p))
	if t := postText(p, 200); t != "" {
		fmt.Fprintf(&b, "%s\n", t)
	}
	ts, _ := p["targets"].([]any)
	for _, raw := range ts {
		t, _ := raw.(map[string]any)
		name := str(t, "channel_name")
		if name == "" {
			name = str(t, "channel")
		}
		line := fmt.Sprintf("  %s (%s): %s", name, str(t, "provider"), strings.ReplaceAll(str(t, "status"), "_", " "))
		if l := str(t, "permalink"); l != "" {
			line += "  " + l
		}
		if e, ok := t["error"].(map[string]any); ok {
			line += "  " + str(e, "message")
		}
		fmt.Fprintf(&b, "%s  [%s]\n", line, str(t, "id"))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// errPreviewInvalid fails a preview that would be refused, so a script can
// stop on it.
var errPreviewInvalid = errors.New("the post does not fit every channel")

// printPreview writes each channel's text, and every problem, for a person.
func printPreview(w io.Writer, raw json.RawMessage) error {
	var pv struct {
		Valid      bool `json:"valid"`
		Renditions []struct {
			Provider   string   `json:"provider"`
			Channel    string   `json:"channel"`
			Parts      []string `json:"parts"`
			Lengths    []int    `json:"lengths"`
			Limit      int      `json:"limit"`
			Violations []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"violations"`
		} `json:"renditions"`
	}
	if err := json.Unmarshal(raw, &pv); err != nil {
		return err
	}
	var b strings.Builder
	for _, r := range pv.Renditions {
		fmt.Fprintf(&b, "%s", r.Provider)
		if r.Channel != "" {
			fmt.Fprintf(&b, " (%s)", r.Channel)
		}
		b.WriteString("\n")
		for i, part := range r.Parts {
			n := 0
			if i < len(r.Lengths) {
				n = r.Lengths[i]
			}
			fmt.Fprintf(&b, "  [%d/%d] %s\n", n, r.Limit, strings.ReplaceAll(part, "\n", "\n        "))
		}
		for _, v := range r.Violations {
			fmt.Fprintf(&b, "  ✗ %s: %s\n", v.Code, v.Message)
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if !pv.Valid {
		return errPreviewInvalid
	}
	return nil
}
