// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// Device sign-in (ADR 0028): the person confirms their password, types the
// code `araldo auth login` printed, sees what is asking, and approves or
// denies it. The code is always typed, never carried in a link, as GitHub's
// device page asks: typing what your own terminal shows is what proves the
// request is yours, so a link someone sends cannot get an approval with a
// click.

type deviceData struct {
	Device *model.DeviceAuthorization
}

// confirmForDevice sends the person to confirm their password and come
// back to the code form, so approving takes one click.
func confirmForDevice(c *reqCtx) error {
	return redirect(c, "/confirm?next=/device", "Confirm your password to sign in a device.")
}

func (s *Server) devicePage(c *reqCtx) error {
	if !s.svc.InSudo(c.session) {
		return confirmForDevice(c)
	}
	return s.page(c, "device", "account", "Sign in a device", deviceData{})
}

func (s *Server) deviceSubmit(c *reqCtx) error {
	if !s.svc.InSudo(c.session) {
		return confirmForDevice(c)
	}
	code, action := c.r.PostFormValue("code"), c.r.PostFormValue("action")
	if action == "" {
		// The typed code: show what is asking, to approve or deny.
		dev, err := s.svc.PendingDevice(c.ctx(), code)
		if err != nil {
			return s.formErr(c, "device", "account", "Sign in a device", deviceData{}, err)
		}
		return s.page(c, "device", "account", "Sign in a device", deviceData{Device: dev})
	}
	dev, err := s.svc.DecideDevice(c.ctx(), c.session, code, action == "approve")
	if err != nil {
		if apperr.As(err).Code == "reauthentication_required" {
			return confirmForDevice(c)
		}
		return s.formErr(c, "device", "account", "Sign in a device", deviceData{}, err)
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
