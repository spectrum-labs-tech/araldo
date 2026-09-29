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

const postCollection = "app.bsky.feed.post"

// Adapter publishes to Bluesky.
type Adapter struct {
	Client *http.Client
	// Web is the public web app, for permalinks.
	Web string
}

// New returns a Bluesky adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, Web: "https://bsky.app"}
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
	Langs     []string  `json:"langs,omitempty"`
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
			rec := post{Type: postCollection, Text: p.Parts[i], CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Facets: Facets(p.Parts[i])}
			if root != nil {
				rec.Reply = &replyRef{Root: *root, Parent: *parent}
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

var (
	linkRE = regexp.MustCompile(`https?://[^\s<>"]+[^\s<>".,;:!?)\]]`)
	tagRE  = regexp.MustCompile(`(^|\s)#([\p{L}\p{N}_]+)`)
)

// Facets marks links and hashtags so Bluesky renders them (byte offsets in
// UTF-8, as the protocol requires).
func Facets(text string) []facet {
	var out []facet
	for _, m := range linkRE.FindAllStringIndex(text, -1) {
		out = append(out, facet{Index: byteSlice{m[0], m[1]}, Features: []feature{{Type: "app.bsky.richtext.facet#link", URI: text[m[0]:m[1]]}}})
	}
	for _, m := range tagRE.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[4]-1, m[5] // include the '#'
		out = append(out, facet{Index: byteSlice{start, end}, Features: []feature{{Type: "app.bsky.richtext.facet#tag", Tag: text[m[4]:m[5]]}}})
	}
	return out
}
