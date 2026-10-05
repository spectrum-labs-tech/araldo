// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strconv"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Developer apps and OAuth connections (ADR 0021).

type appsData struct {
	// Apps are the org's; InstallApps the server provides to every org
	// (ADR 0030), which members use but cannot change.
	Apps        []*model.ProviderApp
	InstallApps []*model.ProviderApp
	Providers   []core.ProviderInfo
	Redirects   map[platform.Provider]string
	Form        map[string]string
	// Names are providers' names for people, Added which have an app, and
	// Sites where each registers one (for the guide).
	Names map[platform.Provider]string
	Added map[platform.Provider]bool
	Sites map[platform.Provider]string
	// Guides say how to register each platform's app.
	Guides map[platform.Provider]appGuide
}

// developerSites are where each provider's developer apps are registered.
var developerSites = map[platform.Provider]string{
	platform.X: "https://developer.x.com", platform.LinkedIn: "https://www.linkedin.com/developers/apps",
	platform.LinkedInPages: "https://www.linkedin.com/developers/apps",
	platform.Threads:       "https://developers.facebook.com/apps", platform.Facebook: "https://developers.facebook.com/apps",
	platform.Instagram: "https://developers.facebook.com/apps", platform.Pinterest: "https://developers.pinterest.com/apps/",
	platform.YouTube: "https://console.cloud.google.com/apis/credentials", platform.TikTok: "https://developers.tiktok.com/apps/",
	"reddit_ads": "https://www.reddit.com/prefs/apps",
}

func (s *Server) appsData(c *reqCtx) (*appsData, error) {
	apps, err := s.svc.ProviderApps(c.ctx(), c.actor)
	if err != nil {
		return nil, err
	}
	d := &appsData{Redirects: map[platform.Provider]string{}, Form: map[string]string{},
		Names: map[platform.Provider]string{}, Added: map[platform.Provider]bool{}, Sites: developerSites, Guides: appGuides}
	for _, a := range apps {
		if a.Install {
			d.InstallApps = append(d.InstallApps, a)
		} else {
			d.Apps = append(d.Apps, a)
		}
		d.Added[a.Provider] = true
		d.Names[a.Provider] = s.svc.ProviderName(a.Provider)
	}
	for _, p := range s.svc.Providers(true) {
		if p.OAuth {
			d.Providers = append(d.Providers, p)
			d.Redirects[p.Provider] = s.svc.ConnectRedirectURI(p.Provider)
		}
	}
	for _, n := range s.svc.AdNetworks(true) {
		if n.OAuth {
			d.Providers = append(d.Providers, core.ProviderInfo{Provider: n.Provider, Name: s.svc.ProviderName(n.Provider), OAuth: true})
			d.Redirects[n.Provider] = s.svc.ConnectRedirectURI(n.Provider)
		}
	}
	for _, p := range d.Providers {
		d.Names[p.Provider] = p.Name
	}
	return d, nil
}

func (s *Server) apps(c *reqCtx) error {
	d, err := s.appsData(c)
	if err != nil {
		return err
	}
	return s.page(c, "apps", "channels", "Developer apps", d)
}

func (s *Server) createApp(c *reqCtx) error {
	d, err := s.appsData(c)
	if err != nil {
		return err
	}
	f := c.r.PostForm
	for _, k := range []string{"provider", "name", "client_id"} {
		d.Form[k] = f.Get(k)
	}
	if _, err := s.svc.CreateProviderApp(c.ctx(), c.actor, core.ProviderAppInput{Provider: platform.Provider(f.Get("provider")),
		Name: f.Get("name"), ClientID: f.Get("client_id"), ClientSecret: f.Get("client_secret")}); err != nil {
		return s.formErr(c, "apps", "channels", "Developer apps", d, err)
	}
	return redirect(c, "/channels/apps", "App added. Connect channels through it from Channels.")
}

func (s *Server) renameApp(c *reqCtx) error {
	appID, err := uuid.Parse(c.r.PathValue("id"))
	if err != nil {
		return apperr.NotFound("app")
	}
	app, err := s.svc.RenameProviderApp(c.ctx(), c.actor, appID, c.r.PostFormValue("name"))
	if err != nil {
		d, derr := s.appsData(c)
		if derr != nil {
			return derr
		}
		return s.formErr(c, "apps", "channels", "Developer apps", d, err)
	}
	return redirect(c, "/channels/apps", "Renamed to "+app.Name+".")
}

func (s *Server) deleteApp(c *reqCtx) error {
	appID, err := uuid.Parse(c.r.PathValue("id"))
	if err != nil {
		return apperr.NotFound("app")
	}
	if err := s.svc.DeleteProviderApp(c.ctx(), c.actor, appID); err != nil {
		return err
	}
	return redirect(c, "/channels/apps", "App deleted.")
}

// startConnect begins a sign-in and sends the member to the platform. The
// redirect is a page with a link, not an HTTP redirect after the form: the
// dashboard's Content-Security-Policy keeps form submissions on this site.
func (s *Server) startConnect(c *reqCtx) error {
	q := c.r.URL.Query()
	b, err := s.svc.ResolveBrand(c.ctx(), c.actor, q.Get("brand"))
	if err != nil {
		return err
	}
	appID, err := uuid.Parse(q.Get("app"))
	if err != nil {
		return apperr.Invalid("app_required", "app", "Choose a developer app.")
	}
	target, err := s.svc.BeginConnect(c.ctx(), c.actor, b.ID, appID)
	if err != nil {
		return err
	}
	name := s.svc.ProviderName(platform.Provider(c.r.PathValue("provider")))
	return s.page(c, "connect_redirect", "channels", "Sign in with "+name, map[string]string{"URL": target, "Name": name})
}

type chooseData struct {
	Provider platform.Provider
	Name     string
	State    string
	Choices  []platform.Account
}

// connectCallback is where the platform sends the member back.
func (s *Server) connectCallback(c *reqCtx) error {
	q := c.r.URL.Query()
	provider := platform.Provider(c.r.PathValue("provider"))
	if e := q.Get("error"); e != "" {
		back := "/channels"
		if _, ok := ads.NetworkOf(provider); ok {
			back = "/ads"
		}
		return redirect(c, back, "Not connected: "+firstNonEmpty(q.Get("error_description"), e)+".")
	}
	res, err := s.svc.FinishConnect(c.ctx(), c.actor, provider, q.Get("state"), q.Get("code"))
	if err != nil {
		return err
	}
	if len(res.Choices) > 0 {
		return s.page(c, "connect_choose", "channels", "Choose accounts",
			chooseData{Provider: provider, Name: s.svc.ProviderName(provider), State: res.State, Choices: res.Choices})
	}
	return connected(c, res)
}

func (s *Server) chooseConnections(c *reqCtx) error {
	res, err := s.svc.ChooseConnections(c.ctx(), c.actor, platform.Provider(c.r.PathValue("provider")), c.r.PostFormValue("state"),
		c.r.PostForm["account"])
	if err != nil {
		return err
	}
	return connected(c, res)
}

// connected goes back to where the sign-in started: Ads for ad accounts,
// Channels for channels.
func connected(c *reqCtx, res *core.ConnectResult) error {
	switch {
	case len(res.AdAccounts) == 1:
		return redirect(c, "/ads", "Connected "+res.AdAccounts[0].Name+". Its results are read within a few minutes, then daily.")
	case len(res.AdAccounts) > 1:
		return redirect(c, "/ads", "Connected "+itoa(len(res.AdAccounts))+" ad accounts. Their results are read within a few minutes, then daily.")
	case len(res.Channels) == 1:
		return redirect(c, "/channels", "Connected "+res.Channels[0].DisplayName+".")
	}
	return redirect(c, "/channels", "Connected "+itoa(len(res.Channels))+" channels.")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func itoa(n int) string { return strconv.Itoa(n) }
