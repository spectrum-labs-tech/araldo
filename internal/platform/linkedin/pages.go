// SPDX-License-Identifier: AGPL-3.0-or-later

package linkedin

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Pages posts as LinkedIn company Pages, through LinkedIn's Community
// Management API, which LinkedIn reviews and grants only to an app with no
// other products: so it is its own platform, with its own developer app,
// beside LinkedIn (a member's feed). A channel is one Page the member who
// signs in administers. Posting, images and video are LinkedIn's, with the
// Page as author.
type Pages struct {
	*Adapter
}

// pageScopes are the least the Community Management API needs to list the
// Pages a member administers (r_organization_admin) and post as them
// (w_organization_social)
// (https://learn.microsoft.com/en-us/linkedin/marketing/community-management/organizations/organization-access-control-by-role).
const pageScopes = "r_organization_admin w_organization_social"

// NewPages returns a LinkedIn Pages adapter.
func NewPages(client *http.Client) *Pages { return &Pages{Adapter: New(client)} }

func (p *Pages) Provider() platform.Provider { return platform.LinkedInPages }

func (p *Pages) Rules() platform.Rules {
	r, _ := platform.RulesFor(platform.LinkedInPages)
	return r
}

func (p *Pages) Fields() []platform.Field {
	return []platform.Field{
		{Name: "access_token", Label: "Access token", Secret: true,
			Help: "linkedin.com/developers → your Pages app → Docs and tools → OAuth token tools, with r_organization_admin and w_organization_social. It lasts 60 days."},
		{Name: "organization", Label: "Page ID",
			Help: "The number in the Page's admin address, linkedin.com/company/<number>/admin"},
		{Name: "api_version", Label: "API version", Optional: true, Default: DefaultVersion,
			Help: "LinkedIn-Version (YYYYMM); change it when LinkedIn retires this one"},
	}
}

func (p *Pages) AuthorizeURL(app platform.App, redirectURI, state, _ string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {app.ClientID}, "redirect_uri": {redirectURI}, "scope": {pageScopes}, "state": {state}}
	return p.Auth + "/authorization?" + q.Encode()
}

// Exchange trades the code for a token and offers each Page the member
// administers as a channel.
func (p *Pages) Exchange(ctx context.Context, app platform.App, redirectURI, code, _ string) ([]platform.Connection, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {app.ClientID}, "client_secret": {app.ClientSecret}}
	t, err := platform.RequestToken(ctx, p.Client, p.Auth+"/accessToken", form, nil)
	if err != nil {
		return nil, err
	}
	base := platform.Credentials{"access_token": t.AccessToken}
	if t.RefreshToken != "" {
		base["refresh_token"] = t.RefreshToken
	}
	h, err := p.headers(base)
	if err != nil {
		return nil, err
	}
	ids, err := p.administered(ctx, h)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, platform.Errorf(platform.Rejected, "this LinkedIn account administers no Pages: ask a Page's admin to make it one")
	}
	out := make([]platform.Connection, 0, len(ids))
	for _, id := range ids {
		c := platform.Credentials{"organization": id}
		for k, v := range base {
			c[k] = v
		}
		acct, err := p.page(ctx, h, id)
		if err != nil {
			return nil, err
		}
		out = append(out, platform.Connection{Account: acct, Credentials: c, ExpiresAt: t.Expiry(p.Now())})
	}
	return out, nil
}

// administered lists the IDs of the Pages the token's member administers
// (organizationAcls by role assignee, approved administrators).
func (p *Pages) administered(ctx context.Context, h map[string]string) ([]string, error) {
	var acls struct {
		Elements []struct {
			Organization string `json:"organization"`
		} `json:"elements"`
	}
	q := url.Values{"q": {"roleAssignee"}, "role": {"ADMINISTRATOR"}, "state": {"APPROVED"}}
	if err := platform.JSON(ctx, p.Client, http.MethodGet, p.API+"/rest/organizationAcls?"+q.Encode(), h, nil, &acls); err != nil {
		return nil, expired(err)
	}
	var ids []string
	for _, e := range acls.Elements {
		if id, ok := strings.CutPrefix(e.Organization, "urn:li:organization:"); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// page reads a Page's name and vanity name.
func (p *Pages) page(ctx context.Context, h map[string]string, id string) (platform.Account, error) {
	var org struct {
		LocalizedName string `json:"localizedName"`
		VanityName    string `json:"vanityName"`
	}
	if err := platform.JSON(ctx, p.Client, http.MethodGet, p.API+"/rest/organizations/"+url.PathEscape(id), h, nil, &org); err != nil {
		return platform.Account{}, expired(err)
	}
	name := org.LocalizedName
	if name == "" {
		name = "Page " + id
	}
	acct := platform.Account{ExternalID: "urn:li:organization:" + id, Handle: org.VanityName, DisplayName: name + " (LinkedIn Page)"}
	if org.VanityName != "" {
		acct.URL = "https://www.linkedin.com/company/" + org.VanityName + "/"
	}
	return acct, nil
}

// pageID is the channel's Page.
func pageID(c platform.Credentials) (string, error) {
	id := strings.TrimSpace(c["organization"])
	id = strings.TrimPrefix(id, "urn:li:organization:")
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return "", platform.Errorf(platform.Rejected, "the Page ID is the number in its admin address, linkedin.com/company/<number>/admin")
	}
	return id, nil
}

// Verify checks the token can read the Page, and names it.
func (p *Pages) Verify(ctx context.Context, c platform.Credentials) (platform.Account, error) {
	id, err := pageID(c)
	if err != nil {
		return platform.Account{}, err
	}
	h, err := p.headers(c)
	if err != nil {
		return platform.Account{}, err
	}
	return p.page(ctx, h, id)
}

// Publish posts as the Page.
func (p *Pages) Publish(ctx context.Context, c platform.Credentials, pl platform.Payload, onPart func(platform.RemoteRef) error) (platform.Result, error) {
	id, err := pageID(c)
	if err != nil {
		return platform.Result{Parts: pl.Posted}, err
	}
	return p.publish(ctx, c, pl, onPart, func(map[string]string) (string, error) { return "urn:li:organization:" + id, nil })
}
