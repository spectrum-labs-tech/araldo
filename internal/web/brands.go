// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/http"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
	"github.com/spectrum-labs-tech/araldo/internal/newsletter"
)

// A brand's pages: an overview, its settings and its email theme, each
// linked from the others in the sidebar (under Brands) and, on a phone,
// above the page.

// brandNav is the brand whose pages the sidebar lists, and which is shown.
type brandNav struct {
	ID, Name string
	// Current is "overview", "settings" or "theme".
	Current string
}

// brandPages are a brand's pages, in order, for the sub-navigation.
var brandPages = []struct{ Key, Label, Suffix string }{
	{"overview", "Overview", ""}, {"settings", "Settings", "/settings"}, {"theme", "Email theme", "/email-theme"},
}

// renderBrand shows one of brand b's pages, with err as its form's
// problem (nil for none).
func (s *Server) renderBrand(c *reqCtx, page, title string, data any, b *model.Brand, current string, err error) error {
	v := s.view(c, "brands", title, data)
	v.BrandNav = &brandNav{ID: id.Format(id.Brand, b.ID), Name: b.Name, Current: current}
	status := http.StatusOK
	if err != nil {
		ae := apperr.As(err)
		if ae.Kind == apperr.KindInternal || ae.Kind == apperr.KindUnavailable {
			return err
		}
		v.Error, v.Problems = ae.Message, ae.Problems
		status = http.StatusUnprocessableEntity
		if ae.Kind == apperr.KindForbidden {
			status = http.StatusForbidden
			s.recordDenial(c, ae)
		}
	}
	s.render(c.w, status, page, v)
	return nil
}

// pathBrand is the brand the path names.
func (s *Server) pathBrand(c *reqCtx) (*model.Brand, error) {
	bid, err := pathUUID(c, id.Brand, "brand")
	if err != nil {
		return nil, err
	}
	return s.svc.Brand(c.ctx(), c.actor, bid)
}

// brandOverview is a brand's page: what it has, at a glance, with links.
type brandOverview struct {
	Brand    *model.Brand
	Channels []*model.Channel
	Senders  []*model.MailAccount
	// PostTemplates and IssueTemplates count the brand's templates by kind.
	PostTemplates, IssueTemplates int
	Posts                         []*model.Post
	Issues                        []*model.Issue
	// Media counts the brand's media, to MediaMore ("100+").
	Media     int
	MediaMore bool
	// ThemeReady: the email theme has the postal address issues need.
	ThemeReady bool
	// Can* say which parts the person may see; a part they may not is left
	// out rather than refused.
	CanPosts, CanIssues, CanMedia bool
	// CanConnect and CanConnectMail offer to connect a channel and add a
	// sender.
	CanConnect, CanConnectMail bool
}

// overviewLimit is how many recent posts and issues the overview lists.
const overviewLimit = 5

func (s *Server) brandOverview(c *reqCtx) error {
	b, err := s.pathBrand(c)
	if err != nil {
		return err
	}
	d := &brandOverview{Brand: b, ThemeReady: b.EmailTheme.PostalAddress != "",
		CanPosts: c.actor.Can(core.PermPostsRead), CanIssues: c.actor.Can(core.PermNewslettersRead)}
	d.CanMedia = d.CanPosts || c.actor.Can(core.PermPostsWrite)
	d.CanConnect = c.actor.Can(core.PermChannelsWrite)
	d.CanConnectMail = c.actor.Can(core.PermNewslettersWrite)
	if c.actor.Can(core.PermChannelsRead) {
		if d.Channels, err = s.svc.Channels(c.ctx(), c.actor, &b.ID); err != nil {
			return err
		}
	}
	if c.actor.Can(core.PermTemplatesRead) {
		tpls, err := s.svc.Templates(c.ctx(), c.actor, &b.ID)
		if err != nil {
			return err
		}
		for _, t := range tpls {
			if t.Kind == model.TemplateNewsletter {
				d.IssueTemplates++
			} else {
				d.PostTemplates++
			}
		}
	}
	if d.CanPosts {
		if d.Posts, _, err = s.svc.Posts(c.ctx(), c.actor, core.PostFilter{BrandID: &b.ID}, core.Page{Limit: overviewLimit}); err != nil {
			return err
		}
	}
	if d.CanIssues {
		if d.Senders, err = s.svc.MailAccounts(c.ctx(), c.actor, &b.ID); err != nil {
			return err
		}
		if d.Issues, _, err = s.svc.Issues(c.ctx(), c.actor, core.IssueFilter{BrandID: &b.ID}, core.Page{Limit: overviewLimit}); err != nil {
			return err
		}
	}
	if d.CanMedia {
		ms, more, err := s.svc.MediaList(c.ctx(), c.actor, core.MediaFilter{BrandID: &b.ID}, core.Page{Limit: 100})
		if err != nil {
			return err
		}
		d.Media, d.MediaMore = len(ms), more
	}
	return s.renderBrand(c, "brand_overview", b.Name, d, b, "overview", nil)
}

func (s *Server) brandSettings(c *reqCtx) error {
	b, err := s.pathBrand(c)
	if err != nil {
		return err
	}
	f, err := s.brandForm(c, b)
	if err != nil {
		return err
	}
	return s.renderBrand(c, "brand_edit", b.Name+": settings", f, b, "settings", nil)
}

func (s *Server) brandTheme(c *reqCtx) error {
	b, err := s.pathBrand(c)
	if err != nil {
		return err
	}
	f, err := s.brandForm(c, b)
	if err != nil {
		return err
	}
	return s.renderBrand(c, "brand_theme", b.Name+": email theme", f, b, "theme", nil)
}

// themeInput reads the email theme form. The logo is the menu's choice;
// a file uploaded beside it is the caller's to store.
func themeInput(c *reqCtx) (core.EmailThemeInput, error) {
	f := c.r.PostForm
	in := core.EmailThemeInput{Accent: f.Get("accent"), PostalAddress: f.Get("postal_address"), Footer: f.Get("footer")}
	if logo := f.Get("logo"); logo != "" {
		mid, err := id.Parse(id.Media, logo)
		if err != nil {
			return in, apperr.Invalid("logo_unknown", "logo", "Choose one of the brand's images.")
		}
		in.LogoMediaID = &mid
	}
	if in.Accent == newsletter.DefaultAccent {
		in.Accent = ""
	}
	return in, nil
}

// themePreview renders the theme form's unsaved values around the sample
// issue, into the preview frame. A logo file chosen but not saved is not
// shown: it is stored only when the theme is saved.
func (s *Server) themePreview(c *reqCtx) error {
	bid, err := pathUUID(c, id.Brand, "brand")
	if err != nil {
		return err
	}
	in, err := themeInput(c)
	var r newsletter.Rendered
	if err == nil {
		r, err = s.svc.PreviewEmailTheme(c.ctx(), c.actor, bid, in)
	}
	if err != nil {
		return s.previewError(c, "previewing an email theme", err)
	}
	return s.writePreview(c, r.HTML)
}

// savedThemePreview is the sample issue in the brand's saved theme: the
// frame's first content, before the form is touched.
func (s *Server) savedThemePreview(c *reqCtx) error {
	b, err := s.pathBrand(c)
	if err != nil {
		return err
	}
	t := b.EmailTheme
	r, err := s.svc.PreviewEmailTheme(c.ctx(), c.actor, b.ID, core.EmailThemeInput{LogoMediaID: t.LogoMediaID, Accent: t.Accent,
		PostalAddress: t.PostalAddress, Footer: t.Footer})
	if err != nil {
		return s.previewError(c, "previewing an email theme", err)
	}
	return s.writePreview(c, r.HTML)
}
