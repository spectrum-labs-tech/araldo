// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Tool is one MCP tool: its contract for the assistant, and how it calls
// the API.
type Tool struct {
	Name        string
	Title       string
	Description string
	Input       map[string]any // JSON Schema of the arguments
	// Hints for the assistant (MCP tool annotations).
	ReadOnly, Destructive, Idempotent bool
	Run                               func(ctx context.Context, args map[string]any) (json.RawMessage, error)
}

func (t Tool) describe() map[string]any {
	return map[string]any{
		"name": t.Name, "title": t.Title, "description": t.Description, "inputSchema": t.Input,
		"annotations": map[string]any{
			"title": t.Title, "readOnlyHint": t.ReadOnly, "destructiveHint": t.Destructive, "idempotentHint": t.Idempotent,
			// Only writing reaches the outside world: social platforms.
			"openWorldHint": !t.ReadOnly,
		},
	}
}

// Schema helpers.

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

func enum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": desc}
}

const (
	brandDesc  = "Brand ID (brand_…) or slug, from list_brands."
	postDesc   = "Post ID (post_…)."
	targetDesc = "One channel's copy of a post (ptgt_…), from get_post's targets."
)

// postProps are the arguments a post takes, for preview and create.
func postProps() map[string]any {
	return map[string]any{
		"brand":    str(brandDesc),
		"body":     str("The post's text, when not using a template. A line containing only {{thread}} starts a new part of a thread."),
		"template": str(`A template: "key", "key@version" or its ID (tmpl_…). Use with data, instead of body.`),
		"data":     map[string]any{"type": "object", "description": "The template's data, matching its variables (see get_template)."},
		"overrides": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
			"description": "Text for particular platforms instead of body, keyed by provider (bluesky, mastodon, x…), e.g. a shorter version."},
		"fit": enum("What to do where the text is too long for a platform: refuse (error, the default), shorten it with an ellipsis "+
			"(truncate), or split it into a thread (thread).", "error", "truncate", "thread"),
		"channels": strs("Channel IDs (chan_…) to post to. Default: every active channel of the brand."),
		"media":    strs("Media IDs (media_…) from upload_media_from_url, attached in order, on the first part of a thread."),
	}
}

// postBody turns post arguments into the API's post body.
func postBody(args map[string]any) map[string]any {
	body := map[string]any{}
	for _, k := range []string{"brand", "template", "data", "channels", "media", "publish_at", "publish_by", "metadata"} {
		if v, ok := args[k]; ok {
			body[k] = v
		}
	}
	text, hasText := args["body"].(string)
	overrides, hasOverrides := args["overrides"].(map[string]any)
	fit, hasFit := args["fit"].(string)
	if hasText || hasOverrides || hasFit {
		content := map[string]any{"body": text}
		if hasOverrides {
			content["overrides"] = overrides
		}
		if hasFit {
			byPlatform := map[string]string{}
			for _, p := range platform.Emulable() {
				byPlatform[string(p)] = fit
			}
			content["fit"] = byPlatform
		}
		body["content"] = content
	}
	return body
}

func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

func required(args map[string]any, key string) (string, error) {
	if s := argString(args, key); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("%s is required", key)
}

// query builds query parameters from the string arguments named.
func query(args map[string]any, keys ...string) url.Values {
	q := url.Values{}
	for _, k := range keys {
		switch v := args[k].(type) {
		case string:
			if v != "" {
				q.Set(k, v)
			}
		case float64:
			q.Set(k, strconv.FormatFloat(v, 'f', -1, 64))
		}
	}
	return q
}

// tools are Araldo's tools, in the order an assistant meets them.
func tools(api *Client) []Tool {
	get := func(path string, q url.Values) func(ctx context.Context) (json.RawMessage, error) {
		return func(ctx context.Context) (json.RawMessage, error) {
			return api.Do(ctx, http.MethodGet, path, q, nil, "")
		}
	}
	return []Tool{
		{
			Name: "list_platforms", Title: "List platforms and their rules", ReadOnly: true, Idempotent: true,
			Description: "Each platform's limits: maximum length and how it is counted, threads, images (types, sizes, aspect " +
				"ratios). preview_post applies them; read this to write text that fits the first time.",
			Input: object(nil, map[string]any{}),
			Run: func(ctx context.Context, _ map[string]any) (json.RawMessage, error) {
				return get("/v1/platforms", nil)(ctx)
			},
		},
		{
			Name: "list_brands", Title: "List brands", ReadOnly: true, Idempotent: true,
			Description: "The brands (products) this key can post for, with their slugs, time zones, approval policies and weekly slots.",
			Input:       object(nil, map[string]any{}),
			Run: func(ctx context.Context, _ map[string]any) (json.RawMessage, error) {
				return get("/v1/brands", nil)(ctx)
			},
		},
		{
			Name: "list_channels", Title: "List channels", ReadOnly: true, Idempotent: true,
			Description: "The connected accounts posts go to: each channel's platform (provider), account and status. Only " +
				"active channels publish.",
			Input: object(nil, map[string]any{"brand": str("Only this brand's channels. " + brandDesc)}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				return get("/v1/channels", query(args, "brand"))(ctx)
			},
		},
		{
			Name: "list_templates", Title: "List templates", ReadOnly: true, Idempotent: true,
			Description: "Saved post templates (without their bodies; get_template has those).",
			Input: object(nil, map[string]any{
				"brand": str("Only this brand's templates. " + brandDesc),
				"key":   str("Only the template with this key."),
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				return get("/v1/templates", query(args, "brand", "key"))(ctx)
			},
		},
		{
			Name: "get_template", Title: "Get a template", ReadOnly: true, Idempotent: true,
			Description: "A template's body, per-platform overrides, the JSON Schema its data must match, and examples.",
			Input: object([]string{"template"}, map[string]any{
				"template": str("The template's ID (tmpl_…), or its key together with brand."),
				"brand":    str("The brand, when template is a key. " + brandDesc),
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				ref, err := required(args, "template")
				if err != nil {
					return nil, err
				}
				if !strings.HasPrefix(ref, "tmpl_") {
					brand := argString(args, "brand")
					if brand == "" {
						return nil, errors.New("give the template's ID, or its key with brand")
					}
					list, err := api.Do(ctx, http.MethodGet, "/v1/templates", url.Values{"brand": {brand}, "key": {ref}}, nil, "")
					if err != nil {
						return nil, err
					}
					var found struct {
						Data []struct {
							ID string `json:"id"`
						} `json:"data"`
					}
					if err := json.Unmarshal(list, &found); err != nil || len(found.Data) == 0 {
						return nil, fmt.Errorf("no template with key %q in brand %q", ref, brand)
					}
					ref = found.Data[0].ID
				}
				return get("/v1/templates/"+url.PathEscape(ref), nil)(ctx)
			},
		},
		{
			Name: "upload_media_from_url", Title: "Add an image or video",
			Description: "Fetches a JPEG, PNG, GIF or WebP image (up to 16 MiB), or an MP4 or QuickTime video (where the server " +
				"stores media in S3; up to its size limit), from a public URL and stores it for posts to attach. Returns its " +
				"media ID. preview_post checks it against each platform's limits.",
			Input: object([]string{"brand", "url"}, map[string]any{
				"brand": str(brandDesc),
				"url":   str("The image's or video's public http(s) URL."),
				"alt":   str("Alt text describing it, for people who cannot see it (up to 1,000 characters). Please give it."),
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				body := map[string]any{"brand": argString(args, "brand"), "url": argString(args, "url")}
				if alt := argString(args, "alt"); alt != "" {
					body["alt"] = alt
				}
				return api.Do(ctx, http.MethodPost, "/v1/media", nil, body, "")
			},
		},
		{
			Name: "preview_post", Title: "Preview a post", ReadOnly: true, Idempotent: true,
			Description: "Renders a post for each channel without saving it, and lists every rule it breaks (too long, too many " +
				"images…) per channel. Fix every violation, then call create_post with the same arguments.",
			Input: object([]string{"brand"}, postProps()),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				return api.Do(ctx, http.MethodPost, "/v1/posts/preview", nil, postBody(args), "")
			},
		},
		{
			Name: "create_post", Title: "Schedule a post",
			Description: "Schedules a post to every chosen channel; the text is frozen as previewed. It may wait for approval " +
				"if the brand requires it. With a test key it reaches only sandbox channels.",
			Input: func() map[string]any {
				props := postProps()
				props["publish_at"] = str(`"now" (default), "next_slot" (the brand's next free weekly slot), or an RFC 3339 time up to a year ahead; a time more than 15 minutes past is refused.`)
				props["publish_by"] = str("RFC 3339 time to give up publishing by (default: a day after publish_at).")
				props["metadata"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
					"description": "Up to 50 string key-value pairs of your own, e.g. {\"campaign\": \"launch\"}."}
				props["idempotency_key"] = str("Any unique string. Sending the same key again returns the first post instead of " +
					"posting twice; give one when you might retry.")
				return object([]string{"brand"}, props)
			}(),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				key := argString(args, "idempotency_key")
				if key == "" {
					key = uuid.NewString()
				}
				return api.Do(ctx, http.MethodPost, "/v1/posts", nil, postBody(args), key)
			},
		},
		{
			Name: "list_posts", Title: "List posts", ReadOnly: true, Idempotent: true,
			Description: "Posts, newest first, with each channel's status and latest engagement.",
			Input: object(nil, map[string]any{
				"brand":  str(brandDesc),
				"status": enum("Only posts in this state.", "pending_approval", "scheduled", "publishing", "published", "partially_published", "failed", "canceled", "rejected"),
				"q":      str("Only posts whose text contains this."),
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "How many (default 20)."},
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				return get("/v1/posts", query(args, "brand", "status", "q", "limit"))(ctx)
			},
		},
		{
			Name: "get_post", Title: "Get a post", ReadOnly: true, Idempotent: true,
			Description: "A post with each channel's copy, status, link, last error and engagement.",
			Input:       object([]string{"post"}, map[string]any{"post": str(postDesc)}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				id, err := required(args, "post")
				if err != nil {
					return nil, err
				}
				return get("/v1/posts/"+url.PathEscape(id), nil)(ctx)
			},
		},
		{
			Name: "cancel_post", Title: "Cancel a post", Destructive: true, Idempotent: true,
			Description: "Stops every channel of a post that has not published yet. What has published stays published.",
			Input:       object([]string{"post"}, map[string]any{"post": str(postDesc)}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				id, err := required(args, "post")
				if err != nil {
					return nil, err
				}
				return api.Do(ctx, http.MethodPost, "/v1/posts/"+url.PathEscape(id)+"/cancel", nil, map[string]any{}, "")
			},
		},
		{
			Name: "reschedule_post", Title: "Move a post",
			Description: "Moves a post that has not started publishing to another time, or swaps it with another post of the same brand. " +
				"A time on one of the brand's slots takes that slot. Approval is kept.",
			Input: object([]string{"post"}, map[string]any{
				"post": str(postDesc),
				"publish_at": str(`"now", "next_slot" (the brand's next free slot; a post waiting for approval takes it when approved), ` +
					"or an RFC 3339 time up to a year ahead; a time more than 15 minutes past is refused."),
				"publish_by": str("RFC 3339 time to give up publishing by (default: a day after publish_at)."),
				"swap_with":  str("Instead of publish_at: the ID of a post to trade places with."),
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				id, err := required(args, "post")
				if err != nil {
					return nil, err
				}
				body := map[string]any{}
				for _, k := range []string{"publish_at", "publish_by", "swap_with"} {
					if v := argString(args, k); v != "" {
						body[k] = v
					}
				}
				return api.Do(ctx, http.MethodPost, "/v1/posts/"+url.PathEscape(id)+"/reschedule", nil, body, "")
			},
		},
		{
			Name: "retry_target", Title: "Publish one channel's copy again",
			Description: "Publishes again one channel's copy of a post (a target, ptgt_…, from get_post) that failed or needs " +
				"attention. A copy needs attention when the platform may or may not have posted it: look at the account first, " +
				"and if it is there, use mark_target_published instead, or it will appear twice.",
			Input: object([]string{"target"}, map[string]any{"target": str(targetDesc)}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				id, err := required(args, "target")
				if err != nil {
					return nil, err
				}
				return api.Do(ctx, http.MethodPost, "/v1/post_targets/"+url.PathEscape(id)+"/retry", nil, map[string]any{}, "")
			},
		},
		{
			Name: "mark_target_published", Title: "Record that a copy did publish", Idempotent: true,
			Description: "Records that one channel's copy of a post (a target, ptgt_…, from get_post) that needs attention was " +
				"published after all, as someone saw on the account. Give its link if known.",
			Input: object([]string{"target"}, map[string]any{
				"target":    str(targetDesc),
				"permalink": str("The post's address on the platform, if known."),
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				id, err := required(args, "target")
				if err != nil {
					return nil, err
				}
				body := map[string]any{}
				if v := argString(args, "permalink"); v != "" {
					body["permalink"] = v
				}
				return api.Do(ctx, http.MethodPost, "/v1/post_targets/"+url.PathEscape(id)+"/mark_published", nil, body, "")
			},
		},
		{
			Name: "engagement_summary", Title: "Summarize engagement", ReadOnly: true, Idempotent: true,
			Description: "Likes, reposts, replies and quotes of posts published in the last days, by post, channel or template, " +
				"most engaging first. Use it to see what works. Clicks are in the brand's web analytics (UTM parameters).",
			Input: object(nil, map[string]any{
				"brand":    str(brandDesc),
				"group_by": enum("How to group (default post).", "post", "channel", "template"),
				"days":     map[string]any{"type": "integer", "minimum": 1, "maximum": 365, "description": "How far back (default 30)."},
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				q := query(args, "brand", "group_by")
				if d, ok := args["days"].(float64); ok && d >= 1 {
					q.Set("since", time.Now().Add(-time.Duration(d)*24*time.Hour).UTC().Format(time.RFC3339))
				}
				return get("/v1/engagement/summary", q)(ctx)
			},
		},
		{
			Name: "ads_summary", Title: "Summarize ad spend", ReadOnly: true, Idempotent: true,
			Description: "Spend, impressions, clicks, cost per click and results of the connected ad accounts' campaigns, by brand, " +
				"account, campaign or day. Amounts are in each row's currency's minor unit (cents). Reading only: Araldo spends nothing. " +
				"Signups are in the brand's web analytics (UTM parameters).",
			Input: object(nil, map[string]any{
				"brand":    str(brandDesc),
				"group_by": enum("How to group (default campaign).", "brand", "account", "campaign", "day"),
				"days":     map[string]any{"type": "integer", "minimum": 1, "maximum": 365, "description": "How far back, ending today (default 30)."},
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				q := query(args, "brand", "group_by")
				if d, ok := args["days"].(float64); ok && d >= 1 {
					q.Set("since", time.Now().UTC().AddDate(0, 0, -(int(d)-1)).Format(time.DateOnly))
				}
				return get("/v1/ads/summary", q)(ctx)
			},
		},
		{
			Name: "analytics_summary", Title: "Summarize visits and signups", ReadOnly: true, Idempotent: true,
			Description: "Visitors and signups (goal completions) from the brand's own web analytics, credited to the post, network, " +
				"campaign or day whose tagged link brought them. Use it to see which posts bring customers, not just likes. " +
				"Counts only; untagged traffic is reported separately.",
			Input: object(nil, map[string]any{
				"brand":    str(brandDesc),
				"group_by": enum("How to group (default post).", "post", "source", "medium", "campaign", "content", "day"),
				"days":     map[string]any{"type": "integer", "minimum": 1, "maximum": 365, "description": "How far back, ending today (default 30)."},
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				q := query(args, "brand", "group_by")
				if d, ok := args["days"].(float64); ok && d >= 1 {
					q.Set("since", time.Now().UTC().AddDate(0, 0, -(int(d)-1)).Format(time.DateOnly))
				}
				return get("/v1/analytics/summary", q)(ctx)
			},
		},
		{
			Name: "preview_newsletter", Title: "Preview a newsletter issue", ReadOnly: true, Idempotent: true,
			Description: "Renders a newsletter issue as the brand's email provider would send it, links tagged, and returns its " +
				"plain-text version (and HTML) without saving anything. Use it to check a draft. " + newsletterBodyHelp,
			Input: object([]string{"brand", "subject", "body"}, newsletterProps()),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				return api.Do(ctx, http.MethodPost, "/v1/newsletters/preview", nil, newsletterBody(args), "")
			},
		},
		{
			Name: "draft_newsletter", Title: "Draft a newsletter issue",
			Description: "Saves a newsletter issue as a draft, going to each of the brand's mail accounts with their default " +
				"audiences. A person reviews, schedules and approves it in Araldo; nothing is sent from here. " + newsletterBodyHelp,
			Input: func() map[string]any {
				props := newsletterProps()
				props["idempotency_key"] = str("Any unique string. Sending the same key again returns the first draft.")
				return object([]string{"brand", "subject", "body"}, props)
			}(),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				key := argString(args, "idempotency_key")
				if key == "" {
					key = uuid.NewString()
				}
				return api.Do(ctx, http.MethodPost, "/v1/newsletters", nil, newsletterBody(args), key)
			},
		},
		{
			Name: "list_newsletters", Title: "List newsletter issues", ReadOnly: true, Idempotent: true,
			Description: "Newsletter issues, newest first, with each mail account's delivery status and results (delivered, " +
				"clicks, unsubscribes). Clicks are the better signal; opens are inflated by mail apps.",
			Input: object(nil, map[string]any{
				"brand":  str(brandDesc),
				"status": enum("Only issues in this state.", "draft", "pending_approval", "scheduled", "sending", "sent", "partially_sent", "canceled", "failed"),
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "How many (default 20)."},
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				return get("/v1/newsletters", query(args, "brand", "status", "limit"))(ctx)
			},
		},
		{
			Name: "brand_report", Title: "Report on a brand's month", ReadOnly: true, Idempotent: true,
			Description: "A brand's results for a month (or any period) beside the period before: posts published, engagement, " +
				"web visitors and signups, ad spend and cost per signup, and newsletters sent. Use it to write the month's summary " +
				"for a team or a client. Sections the key may not see, or that are empty, are null.",
			Input: object([]string{"brand"}, map[string]any{
				"brand": str(brandDesc),
				"month": str("A calendar month, YYYY-MM, in the brand's time zone (default: the last full month)."),
				"since": str("The first day, YYYY-MM-DD, instead of a month."),
				"until": str("The last day, YYYY-MM-DD."),
			}),
			Run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
				return get("/v1/reports", query(args, "brand", "month", "since", "until"))(ctx)
			},
		},
	}
}

// newsletterBodyHelp is the body syntax, for the newsletter tools.
const newsletterBodyHelp = "The body is Markdown: # headings, paragraphs, - lists, > quotes, --- dividers, " +
	"![alt](media_… or https://…) images and [Label](https://…){.button} buttons on lines of their own, and **bold**, *italic* and links."

func newsletterProps() map[string]any {
	return map[string]any{
		"brand":        str(brandDesc),
		"subject":      str("The subject line; under 50 characters reads whole on phones."),
		"preview_text": str("The line inboxes show after the subject."),
		"body":         str("The issue, in the Markdown described above."),
	}
}

func newsletterBody(args map[string]any) map[string]any {
	body := map[string]any{}
	for _, k := range []string{"brand", "subject", "preview_text", "body"} {
		if v := argString(args, k); v != "" {
			body[k] = v
		}
	}
	return body
}
