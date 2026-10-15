// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mastodon publishes to any Mastodon server with an access token
// the account owner creates (Preferences → Development, scopes
// write:statuses, write:media and read:accounts).
//
// Mastodon honors an Idempotency-Key header for an hour, so a retry after
// an uncertain failure within that hour cannot post twice; after it, a
// retry would be a second post (see IdempotencyWindow).
package mastodon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Adapter publishes to Mastodon.
type Adapter struct {
	Client *http.Client
	// Poll is how long to wait between checks on media the server is
	// still processing, and MaxPolls how many checks to make; tests
	// shorten them.
	Poll     time.Duration
	MaxPolls int
}

// maxPolls at the 1-second Poll waits five minutes for a video to process.
const maxPolls = 300

// New returns a Mastodon adapter.
func New(client *http.Client) *Adapter {
	return &Adapter{Client: client, Poll: time.Second, MaxPolls: maxPolls}
}

func (a *Adapter) Provider() platform.Provider { return platform.Mastodon }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Mastodon)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{
		{Name: "instance", Label: "Server URL", Help: "for example https://mastodon.social"},
		{Name: "access_token", Label: "Access token", Help: "Preferences → Development → New application (write:statuses, write:media, read:accounts)", Secret: true},
	}
}

func (a *Adapter) Idempotent() bool { return true }

// IdempotencyWindow is how long a retry with the same Idempotency-Key
// cannot post twice. Mastodon keeps a key for an hour
// (https://docs.joinmastodon.org/methods/statuses/#create, "Idempotency-Key");
// five minutes less leaves room for the retry itself to arrive.
func (a *Adapter) IdempotencyWindow() time.Duration { return 55 * time.Minute }

func instance(c platform.Credentials) (string, error) {
	raw := strings.TrimRight(strings.TrimSpace(c["instance"]), "/")
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", platform.Errorf(platform.AuthRevoked, "server URL %q is not valid", c["instance"])
	}
	return u.Scheme + "://" + u.Host, nil
}

func headers(c platform.Credentials) map[string]string {
	return map[string]string{"Authorization": "Bearer " + c["access_token"]}
}

type account struct {
	ID          string `json:"id"`
	Acct        string `json:"acct"`
	DisplayName string `json:"display_name"`
	URL         string `json:"url"`
}

func (a *Adapter) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	base, err := instance(c)
	if err != nil {
		return platform.Account{}, err
	}
	if c["access_token"] == "" {
		return platform.Account{}, platform.Errorf(platform.AuthRevoked, "access token is required")
	}
	var acct account
	if err := platform.JSON(ctx, a.Client, http.MethodGet, base+"/api/v1/accounts/verify_credentials", headers(c), nil, &acct); err != nil {
		return platform.Account{}, err
	}
	host := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	name := acct.DisplayName
	if name == "" {
		name = acct.Acct
	}
	return platform.Account{ExternalID: acct.ID, Handle: "@" + acct.Acct + "@" + host, DisplayName: name, URL: acct.URL}, nil
}

type status struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func (a *Adapter) Publish(ctx context.Context, c platform.Credentials, p platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	base, err := instance(c)
	if err != nil {
		return platform.Result{}, err
	}
	res := platform.Result{Parts: append([]platform.RemoteRef(nil), p.Posted...)}
	replyTo := ""
	if len(p.Posted) > 0 {
		replyTo = p.Posted[len(p.Posted)-1].ID
	}
	for i := len(p.Posted); i < len(p.Parts); i++ {
		h := headers(c)
		h["Idempotency-Key"] = fmt.Sprintf("%s-%d", p.Key, i)
		body := map[string]any{"status": p.Parts[i], "visibility": "public"}
		if replyTo != "" {
			body["in_reply_to_id"] = replyTo
		}
		if i == 0 && len(p.Media) > 0 {
			// An attachment no status uses is not public: uploading it
			// again is safe.
			ids, err := a.upload(platform.RetrySafe(ctx), base, c, p.Media)
			if err != nil {
				return res, err
			}
			body["media_ids"] = ids
		}
		var st status
		if err := platform.JSON(ctx, a.Client, http.MethodPost, base+"/api/v1/statuses", h, body, &st); err != nil {
			return res, err
		}
		if err := platform.RequireID(st.ID, "Mastodon"); err != nil {
			return res, err
		}
		ref := platform.RemoteRef{ID: st.ID, URL: st.URL}
		if onPart != nil {
			if err := onPart(ref); err != nil {
				return res, err
			}
		}
		res.Parts = append(res.Parts, ref)
		replyTo = st.ID
	}
	if len(res.Parts) > 0 {
		res.Permalink = res.Parts[0].URL
	}
	return res, nil
}

type attachment struct {
	ID  string  `json:"id"`
	URL *string `json:"url"`
}

// upload attaches each image, waiting for any the server is still
// processing: a status cannot use an attachment before it is ready
// (https://docs.joinmastodon.org/methods/media/#v2). Attachments no status
// uses are deleted by the server, so an upload before a failed post costs
// nothing.
func (a *Adapter) upload(ctx context.Context, base string, c platform.Credentials, media []platform.Media) ([]string, error) {
	ids := make([]string, 0, len(media))
	for i, m := range media {
		var att attachment
		err := a.send(ctx, base, c, i, m, &att)
		var pe *platform.Error
		if errors.As(err, &pe) && pe.Kind == platform.Rejected && pe.Status == http.StatusForbidden {
			pe.Code = "scope_missing"
			pe.Msg = "the access token cannot upload images: create one with the write:media scope and reconnect (" + pe.Msg + ")"
		}
		if err != nil {
			return nil, err
		}
		if att.ID == "" {
			return nil, &platform.Error{Kind: platform.Transient, Code: "no_id", Msg: "the server returned no attachment for the media"}
		}
		for polls := 0; att.URL == nil || *att.URL == ""; polls++ {
			if polls == max(a.MaxPolls, 1) {
				return nil, &platform.Error{Kind: platform.Transient, Code: "media_processing", Msg: "the server was still processing the media"}
			}
			select {
			case <-ctx.Done():
				return nil, &platform.Error{Kind: platform.Transient, Code: "media_processing", Msg: "the server was still processing the media", Err: ctx.Err()}
			case <-time.After(a.Poll):
			}
			if err := platform.JSON(ctx, a.Client, http.MethodGet, base+"/api/v1/media/"+url.PathEscape(att.ID), headers(c), nil, &att); err != nil {
				return nil, err
			}
		}
		ids = append(ids, att.ID)
	}
	return ids, nil
}

// Engagement reads each status's like, boost, reply and quote counts
// (https://docs.joinmastodon.org/entities/Status/). A status the server
// answers 404 for was deleted.
func (a *Adapter) Engagement(ctx context.Context, c platform.Credentials, refs []platform.RemoteRef) (map[string]platform.Counts, error) {
	base, err := instance(c)
	if err != nil {
		return nil, err
	}
	out := map[string]platform.Counts{}
	for _, r := range refs {
		var st struct {
			Likes   int64 `json:"favourites_count"` //nolint:misspell // the API's spelling
			Reblogs int64 `json:"reblogs_count"`
			Replies int64 `json:"replies_count"`
			Quotes  int64 `json:"quotes_count"`
		}
		err := platform.JSON(ctx, a.Client, http.MethodGet, base+"/api/v1/statuses/"+url.PathEscape(r.ID), headers(c), nil, &st)
		if platform.HTTPStatus(err) == http.StatusNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[r.ID] = platform.Counts{Likes: st.Likes, Reposts: st.Reblogs, Replies: st.Replies, Quotes: st.Quotes}
	}
	return out, nil
}

// send uploads one file: an image from memory, or a video streamed.
func (a *Adapter) send(ctx context.Context, base string, c platform.Credentials, i int, m platform.Media, att *attachment) error {
	fields := [][2]string{{"description", m.Alt}}
	if !m.IsVideo() {
		data, err := m.Read(ctx)
		if err != nil {
			return err
		}
		body, contentType, err := platform.Multipart(fields, []platform.File{{Field: "file", Name: m.Filename(i), Type: m.Type, Data: data}})
		if err != nil {
			return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
		}
		return platform.Send(ctx, a.Client, http.MethodPost, base+"/api/v2/media", headers(c), body, contentType, att)
	}
	rc, err := platform.OpenVideo(ctx, m)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	body, contentType, length, err := platform.MultipartStream(fields, platform.StreamFile{Field: "file", Name: m.Filename(i), Type: m.Type, Size: m.Size, Body: rc})
	if err != nil {
		return &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
	}
	return platform.SendStream(ctx, a.Client, http.MethodPost, base+"/api/v2/media", headers(c), body, length, contentType, att)
}
