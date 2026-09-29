// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mastodon publishes to any Mastodon server with an access token
// the account owner creates (Preferences → Development, scope
// write:statuses and read:accounts).
//
// Mastodon honors an Idempotency-Key header for an hour, so a retry after
// an uncertain failure within that hour cannot post twice.
package mastodon

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Adapter publishes to Mastodon.
type Adapter struct {
	Client *http.Client
}

// New returns a Mastodon adapter.
func New(client *http.Client) *Adapter { return &Adapter{Client: client} }

func (a *Adapter) Provider() platform.Provider { return platform.Mastodon }

func (a *Adapter) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.Mastodon)
	return r
}

func (a *Adapter) Fields() []platform.Field {
	return []platform.Field{
		{Name: "instance", Label: "Server URL", Help: "for example https://mastodon.social"},
		{Name: "access_token", Label: "Access token", Help: "Preferences → Development → New application (write:statuses, read:accounts)", Secret: true},
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
