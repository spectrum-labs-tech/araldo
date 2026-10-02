// SPDX-License-Identifier: AGPL-3.0-or-later

// Package bluesky publishes to Bluesky (AT Protocol) with an app password.
//
// Posts use record keys derived from the target, so a retry after an
// uncertain failure first looks the record up and never creates it twice:
// the adapter is idempotent.
package bluesky

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// DefaultService is Bluesky's main PDS entry point.
const DefaultService = "https://bsky.social"

// DefaultAppView serves public reads (engagement) without signing in.
const DefaultAppView = "https://public.api.bsky.app"

const postCollection = "app.bsky.feed.post"

// Adapter publishes to Bluesky.
type Adapter struct {
	Client *http.Client
	// Web is the public web app, for permalinks.
	Web string
	// AppView answers public reads; tests replace it.
	AppView string
}

// New returns a Bluesky adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, Web: "https://bsky.app", AppView: DefaultAppView}
}

func (a *Adapter) Provider() platform.Provider { return platform.Bluesky }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Bluesky)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{
		{Name: "identifier", Label: "Handle or email", Help: "for example araldo.bsky.social"},
		{Name: "app_password", Label: "App password", Help: "Settings → Privacy and security → App passwords", Secret: true},
		{Name: "service", Label: "PDS URL", Optional: true, Default: DefaultService},
	}
}

func (a *Adapter) Idempotent() bool { return true }

type session struct {
	DID        string `json:"did"`
	Handle     string `json:"handle"`
	AccessJwt  string `json:"accessJwt"`
	RefreshJwt string `json:"refreshJwt"`
}

func service(c platform.Credentials) string {
	if s := strings.TrimRight(c["service"], "/"); s != "" {
		return s
	}
	return DefaultService
}

func (a *Adapter) login(ctx context.Context, c platform.Credentials) (session, error) {
	if c["identifier"] == "" || c["app_password"] == "" {
		return session{}, platform.Errorf(platform.AuthRevoked, "identifier and app password are required")
	}
	var s session
	err := platform.JSON(ctx, a.Client, http.MethodPost, service(c)+"/xrpc/com.atproto.server.createSession", nil,
		map[string]string{"identifier": c["identifier"], "password": c["app_password"]}, &s)
	var pe *platform.Error
	if errors.As(err, &pe) && pe.Kind == platform.Rejected {
		// createSession answers bad credentials with 401, but a malformed
		// identifier with 400: both mean the channel must be reconnected.
		pe.Kind = platform.AuthRevoked
	}
	return s, err
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	s, err := a.login(ctx, c)
	if err != nil {
		return platform.Account{}, err
	}
	return platform.Account{ExternalID: s.DID, Handle: s.Handle, DisplayName: "@" + s.Handle, URL: a.Web + "/profile/" + s.Handle}, nil
}

type strongRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

type replyRef struct {
	Root   strongRef `json:"root"`
	Parent strongRef `json:"parent"`
}

type post struct {
	Type      string    `json:"$type"`
	Text      string    `json:"text"`
	CreatedAt string    `json:"createdAt"`
	Facets    []facet   `json:"facets,omitempty"`
	Reply     *replyRef `json:"reply,omitempty"`
	Embed     *images   `json:"embed,omitempty"`
	Langs     []string  `json:"langs,omitempty"`
}

// images is an app.bsky.embed.images embed.
type images struct {
	Type   string  `json:"$type"`
	Images []image `json:"images"`
}

type image struct {
	Alt         string          `json:"alt"`
	Image       json.RawMessage `json:"image"`
	AspectRatio *aspectRatio    `json:"aspectRatio,omitempty"`
}

type aspectRatio struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type facet struct {
	Index    byteSlice `json:"index"`
	Features []feature `json:"features"`
}

type byteSlice struct {
	ByteStart int `json:"byteStart"`
	ByteEnd   int `json:"byteEnd"`
}

type feature struct {
	Type string `json:"$type"`
	URI  string `json:"uri,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	s, err := a.login(ctx, c)
	if err != nil {
		return platform.Result{}, err
	}
	auth := map[string]string{"Authorization": "Bearer " + s.AccessJwt}
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	var root, parent *strongRef
	if len(p.Posted) > 0 {
		root = &strongRef{URI: p.Posted[0].Extra["uri"], CID: p.Posted[0].Extra["cid"]}
		last := p.Posted[len(p.Posted)-1]
		parent = &strongRef{URI: last.Extra["uri"], CID: last.Extra["cid"]}
	}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		rkey := TID(p.KeyTime, p.Key, i)
		ref, err := a.existing(ctx, c, s, rkey)
		if err != nil {
			return res, err
		}
		if ref == nil {
			text, facets := RichText(p.Parts[i])
			rec := post{Type: postCollection, Text: text, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Facets: facets}
			if root != nil {
				rec.Reply = &replyRef{Root: *root, Parent: *parent}
			}
			if i == 0 && len(p.Media) > 0 {
				if rec.Embed, err = a.upload(ctx, c, auth, p.Media); err != nil {
					return res, err
				}
			}
			var out strongRef
			err := platform.JSON(ctx, a.Client, http.MethodPost, service(c)+"/xrpc/com.atproto.repo.createRecord", auth,
				map[string]any{"repo": s.DID, "collection": postCollection, "rkey": rkey, "record": rec}, &out)
			if err != nil {
				return res, err
			}
			ref = &out
		}
		remote := platform.RemoteRef{
			ID:    ref.URI,
			URL:   a.Web + "/profile/" + s.Handle + "/post/" + rkey,
			Extra: map[string]string{"uri": ref.URI, "cid": ref.CID},
		}
		if onPart != nil {
			if err := onPart(remote); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, remote)
		if root == nil {
			root = ref
		}
		parent = ref
	}
	if len(res.Parts) > 0 {
		res.Permalink = res.Parts[0].URL
	}
	return res, nil
}

// upload stores each image as a blob and returns the embed that shows
// them. A blob no record uses is deleted by the server, so an upload
// before a failed post costs nothing.
func (a *Adapter) upload(ctx context.Context, c platform.Credentials, auth map[string]string, media []platform.Media) (*images, error) {
	embed := &images{Type: "app.bsky.embed.images"}
	for _, m := range media {
		data, err := m.Read(ctx)
		if err != nil {
			return nil, err
		}
		var out struct {
			Blob json.RawMessage `json:"blob"`
		}
		if err := platform.Send(ctx, a.Client, http.MethodPost, service(c)+"/xrpc/com.atproto.repo.uploadBlob", auth, data, m.Type, &out); err != nil {
			return nil, err
		}
		if len(out.Blob) == 0 {
			return nil, platform.Errorf(platform.Transient, "uploadBlob returned no blob")
		}
		img := image{Alt: m.Alt, Image: out.Blob}
		if m.Width > 0 && m.Height > 0 {
			img.AspectRatio = &aspectRatio{Width: m.Width, Height: m.Height}
		}
		embed.Images = append(embed.Images, img)
	}
	return embed, nil
}

// existing returns the record at rkey if an earlier attempt created it.
func (a *Adapter) existing(ctx context.Context, c platform.Credentials, s session, rkey string) (*strongRef, error) {
	q := url.Values{"repo": {s.DID}, "collection": {postCollection}, "rkey": {rkey}}
	var out strongRef
	err := platform.JSON(ctx, a.Client, http.MethodGet, service(c)+"/xrpc/com.atproto.repo.getRecord?"+q.Encode(),
		map[string]string{"Authorization": "Bearer " + s.AccessJwt}, nil, &out)
	var pe *platform.Error
	switch {
	case err == nil:
		return &out, nil
	case errors.As(err, &pe) && pe.Kind == platform.Rejected:
		return nil, nil // RecordNotFound (HTTP 400)
	default:
		return nil, err
	}
}

const tidAlphabet = "234567abcdefghijklmnopqrstuvwxyz"

// TID makes a record key in the AT Protocol TID format: 53 bits of
// microseconds since the epoch and a 10-bit clock ID, as 13 sortable
// base32 characters. It is derived from the target's creation time, its
// key and the part index, so it is the same on every retry.
func TID(t time.Time, key string, part int) string {
	micros := uint64(t.UnixMicro()) + uint64(part) //nolint:gosec // times after 1970
	sum := sha256.Sum256([]byte(key))
	clock := uint64(binary.BigEndian.Uint16(sum[:2]) & 0x3ff)
	v := (micros&(1<<53-1))<<10 | clock
	out := make([]byte, 13)
	for i := 12; i >= 0; i-- {
		out[i] = tidAlphabet[v&31]
		v >>= 5
	}
	return string(out)
}

var tagRE = regexp.MustCompile(`(^|\s)#([\p{L}\p{N}_]+)`)

// RichText returns the text to post, with each link in the short form the
// Bluesky app shows, and the facets that make its links and hashtags work:
// a link facet carries the full URL (byte offsets in UTF-8, as the protocol
// requires). The platform's length rule measures the same short text.
func RichText(raw string) (string, []facet) {
	text, links := platform.ShortenLinks(raw)
	var out []facet
	for _, l := range links {
		out = append(out, facet{Index: byteSlice{l.Start, l.End}, Features: []feature{{Type: "app.bsky.richtext.facet#link", URI: l.URL}}})
	}
	for _, m := range tagRE.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[4]-1, m[5] // include the '#'
		out = append(out, facet{Index: byteSlice{start, end}, Features: []feature{{Type: "app.bsky.richtext.facet#tag", Tag: text[m[4]:m[5]]}}})
	}
	return text, out
}

// getPostsBatch is the most URIs app.bsky.feed.getPosts takes.
const getPostsBatch = 25

// Engagement reads like, repost, reply and quote counts from the public
// AppView, which needs no session: sign-ins are rate limited far more
// tightly than reads
// (https://docs.bsky.app/docs/api/app-bsky-feed-get-posts).
func (a *Adapter) Engagement(ctx context.Context, _ platform.Credentials, refs []platform.RemoteRef) (map[string]platform.Counts, error) {
	out := map[string]platform.Counts{}
	for start := 0; start < len(refs); start += getPostsBatch {
		q := url.Values{}
		for _, r := range refs[start:min(start+getPostsBatch, len(refs))] {
			q.Add("uris", r.ID)
		}
		var res struct {
			Posts []struct {
				URI         string `json:"uri"`
				LikeCount   int64  `json:"likeCount"`
				RepostCount int64  `json:"repostCount"`
				ReplyCount  int64  `json:"replyCount"`
				QuoteCount  int64  `json:"quoteCount"`
			} `json:"posts"`
		}
		if err := platform.JSON(ctx, a.Client, http.MethodGet, strings.TrimRight(a.AppView, "/")+"/xrpc/app.bsky.feed.getPosts?"+q.Encode(), nil, nil, &res); err != nil {
			return nil, err
		}
		for _, p := range res.Posts {
			out[p.URI] = platform.Counts{Likes: p.LikeCount, Reposts: p.RepostCount, Replies: p.ReplyCount, Quotes: p.QuoteCount}
		}
	}
	return out, nil
}
