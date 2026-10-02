// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"strconv"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// Developer apps and OAuth connections (ADR 0021).

type appsData struct {
	Apps      []*model.ProviderApp
	Providers []core.ProviderInfo
	Redirects map[platform.Provider]string
	Form      map[string]string
}

func (s *Server) appsData(c *reqCtx) (*appsData, error) {
	apps, err := s.svc.ProviderApps(c.ctx(), c.actor)
	if err != nil {
		return nil, err
	}
	d := &appsData{Apps: apps, Redirects: map[platform.Provider]string{}, Form: map[string]string{}}
	for _, p := range s.svc.Providers(true) {
		if p.OAuth {
			d.Providers = append(d.Providers, p)
			d.Redirects[p.Provider] = s.svc.ConnectRedirectURI(p.Provider)
		}
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
	name := c.r.PathValue("provider")
	if r, ok := platform.RulesFor(platform.Provider(name)); ok {
		name = r.Name
	}
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
		return redirect(c, "/channels", "Not connected: "+firstNonEmpty(q.Get("error_description"), e)+".")
	}
	res, err := s.svc.FinishConnect(c.ctx(), c.actor, provider, q.Get("state"), q.Get("code"))
	if err != nil {
		return err
	}
	if len(res.Choices) > 0 {
		name := string(provider)
		if r, ok := platform.RulesFor(provider); ok {
			name = r.Name
		}
		return s.page(c, "connect_choose", "channels", "Choose accounts", chooseData{Provider: provider, Name: name, State: res.State, Choices: res.Choices})
	}
	return redirect(c, "/channels", connectedNotice(res.Channels))
}

func (s *Server) chooseConnections(c *reqCtx) error {
	chs, err := s.svc.ChooseConnections(c.ctx(), c.actor, platform.Provider(c.r.PathValue("provider")), c.r.PostFormValue("state"),
		c.r.PostForm["account"])
	if err != nil {
		return err
	}
	return redirect(c, "/channels", connectedNotice(chs))
}

func connectedNotice(chs []*model.Channel) string {
	if len(chs) == 1 {
		return "Connected " + chs[0].DisplayName + "."
	}
	return "Connected " + itoa(len(chs)) + " channels."
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
