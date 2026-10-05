// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"net/url"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Device sign-in (ADR 0028): the person types the code `araldo auth login`
// printed, sees what is asking, and approves or denies it.

type deviceData struct {
	Code   string
	Device *model.DeviceAuthorization
}

func (s *Server) devicePage(c *reqCtx) error {
	d := deviceData{Code: c.r.URL.Query().Get("code")}
	if d.Code == "" {
		return s.page(c, "device", "account", "Sign in a device", d)
	}
	dev, err := s.svc.PendingDevice(c.ctx(), d.Code)
	if err != nil {
		return s.formErr(c, "device", "account", "Sign in a device", d, err)
	}
	d.Device = dev
	return s.page(c, "device", "account", "Sign in a device", d)
}

func (s *Server) deviceSubmit(c *reqCtx) error {
	code, action := c.r.PostFormValue("code"), c.r.PostFormValue("action")
	if action == "" {
		// The code form: show what is asking before anything is approved.
		return redirect(c, "/device?code="+url.QueryEscape(code), "")
	}
	dev, err := s.svc.DecideDevice(c.ctx(), c.session, code, action == "approve")
	if err != nil {
		if apperr.As(err).Code == "reauthentication_required" {
			return redirect(c, "/confirm?next="+url.QueryEscape("/device?code="+code), "Confirm your password to sign in a device.")
		}
		return s.formErr(c, "device", "account", "Sign in a device", deviceData{Code: code}, err)
	}
	if action != "approve" {
		return redirect(c, "/account", "Denied: "+dev.DeviceName+" was not signed in.")
	}
	return redirect(c, "/account", dev.DeviceName+" is signed in. Return to your terminal.")
}

func (s *Server) revokeDevice(c *reqCtx) error {
	tid, err := pathUUID(c, id.UserToken, "device")
	if err != nil {
		return err
	}
	if err := s.svc.RevokeUserToken(c.ctx(), c.user.ID, tid); err != nil {
		return err
	}
	return redirect(c, "/account", "Signed out.")
}

// accountDevices are the person's signed-in devices, for the account page.
func (s *Server) accountDevices(c *reqCtx) ([]core.UserTokenView, error) {
	ts, err := s.svc.UserTokens(c.ctx(), c.user.ID)
	if err != nil {
		return nil, err
	}
	out := make([]core.UserTokenView, len(ts))
	for i, t := range ts {
		out[i] = core.ViewUserToken(t)
	}
	return out, nil
}
