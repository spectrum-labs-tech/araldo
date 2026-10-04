// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The page `araldo auth login` opens (ADR 0028): one short form that makes
// a key for the CLI, and then that key alone, to paste into the terminal.
// It asks for the password before the form, not after, so nothing typed is
// lost to the confirmation. The CLI keeps a test key and a live key apart,
// as the Stripe CLI does, and asks for one: the key is made in that mode,
// whichever mode the dashboard is showing.

type cliData struct {
	// Device is the computer the CLI named.
	Device string
	// Live is the mode the CLI asked for a key in.
	Live   bool
	Brands []*model.Brand
	// Key is the new key, once made.
	Key string
}

// cliDevice reads the computer the CLI named: printable, and short enough
// for a key name.
func cliDevice(c *reqCtx) string {
	v := c.r.FormValue("device")
	out := make([]rune, 0, len(v))
	for _, r := range v {
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
		if len(out) == 60 {
			break
		}
	}
	return strings.TrimSpace(string(out))
}

// cliLive reads the mode the CLI asked for: live only when it says so.
func cliLive(c *reqCtx) bool { return c.r.FormValue("mode") == "live" }

// cliURL is this page for a device and mode.
func cliURL(d *cliData) string {
	u := "/cli?device=" + url.QueryEscape(d.Device)
	if d.Live {
		u += "&mode=live"
	}
	return u
}

// confirmFirst sends the member to confirm their password and come back
// here, keeping the device and mode.
func confirmFirst(c *reqCtx, d *cliData) error {
	next := cliURL(d)
	return redirect(c, "/confirm?next="+url.QueryEscape(next), "Confirm your password to connect the araldo CLI.")
}

func (s *Server) cliPage(c *reqCtx) error {
	d := &cliData{Device: cliDevice(c), Live: cliLive(c)}
	if !s.svc.InSudo(c.session) {
		return confirmFirst(c, d)
	}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return err
	}
	return s.page(c, "cli", "developers", "Connect the araldo CLI", d)
}

func (s *Server) cliCreateKey(c *reqCtx) error {
	d := &cliData{Device: cliDevice(c), Live: cliLive(c)}
	var err error
	if d.Brands, err = s.svc.Brands(c.ctx(), c.actor); err != nil {
		return err
	}
	in := core.APIKeyInput{Name: c.r.PostFormValue("name"), Livemode: d.Live}
	if ref := c.r.PostFormValue("brand"); ref != "" {
		b, err := s.svc.ResolveBrand(c.ctx(), c.actor, ref)
		if err != nil {
			return s.formErr(c, "cli", "developers", "Connect the araldo CLI", d, err)
		}
		in.BrandID = &b.ID
	}
	plain, _, err := s.svc.CreateAPIKey(c.ctx(), c.actor, c.session, in)
	if err != nil {
		if apperr.As(err).Code == "reauthentication_required" {
			return confirmFirst(c, d)
		}
		return s.formErr(c, "cli", "developers", "Connect the araldo CLI", d, err)
	}
	d.Key = plain
	return s.page(c, "cli", "developers", "Connect the araldo CLI", d)
}
