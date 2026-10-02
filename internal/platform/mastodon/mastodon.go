// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mastodon publishes to any Mastodon server with an access token
// the account owner creates (Preferences → Development, scopes
// write:statuses, write:media and read:accounts).
//
// Mastodon honors an Idempotency-Key header for an hour, so a retry after
// an uncertain failure within that hour cannot post twice.
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
	// Poll is how long to wait between checks on an image the server is
	// still processing; tests shorten it.
	Poll time.Duration
}

// New returns a Mastodon adapter.
func New(client *http.Client) *Adapter { return &Adapter{Client: client, Poll: time.Second} }

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
			ids, err := a.upload(ctx, base, c, p.Media)
			if err != nil {
				return res, err
			}
			body["media_ids"] = ids
		}
		var st status
		if err := platform.JSON(ctx, a.Client, http.MethodPost, base+"/api/v1/statuses", h, body, &st); err != nil {
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
		data, err := m.Read(ctx)
		if err != nil {
			return nil, err
		}
		body, contentType, err := platform.Multipart([][2]string{{"description", m.Alt}},
			[]platform.File{{Field: "file", Name: m.Filename(i), Type: m.Type, Data: data}})
		if err != nil {
			return nil, &platform.Error{Kind: platform.Rejected, Code: "encode", Err: err}
		}
		var att attachment
		err = platform.Send(ctx, a.Client, http.MethodPost, base+"/api/v2/media", headers(c), body, contentType, &att)
		var pe *platform.Error
		if errors.As(err, &pe) && pe.Kind == platform.Rejected && strings.HasPrefix(pe.Msg, "HTTP 403") {
			pe.Code = "scope_missing"
			pe.Msg = "the access token cannot upload images: create one with the write:media scope and reconnect (" + pe.Msg + ")"
		}
		if err != nil {
			return nil, err
		}
		for att.URL == nil || *att.URL == "" {
			select {
			case <-ctx.Done():
				return nil, &platform.Error{Kind: platform.Transient, Code: "media_processing", Msg: "the server was still processing an image", Err: ctx.Err()}
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
