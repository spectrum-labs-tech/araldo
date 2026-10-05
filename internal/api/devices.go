// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net"
	"net/http"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// Device sign-in (ADR 0028, RFC 8628): the CLI starts it and polls for its
// token without a credential; the person approves it in the dashboard.

// throttle limits an unauthenticated route per client address.
func (h *Handler) throttle(r *http.Request) error {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if _, _, _, allowed := h.limiter.take("ip:"+host, time.Now()); !allowed {
		return &apperr.Error{Kind: apperr.KindRateLimited, Code: "rate_limited", Message: "Too many requests; slow down."}
	}
	return nil
}

func (h *Handler) startDevice(w http.ResponseWriter, r *http.Request) error {
	if err := h.throttle(r); err != nil {
		return err
	}
	var body struct {
		DeviceName string `json:"device_name"`
		Livemode   bool   `json:"livemode"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	start, err := h.svc.StartDevice(r.Context(), body.DeviceName, body.Livemode, host)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, start)
	return nil
}

// issuedToken is the token a device sign-in yields, shown this once.
type issuedToken struct {
	Object      string             `json:"object"`
	AccessToken string             `json:"access_token"`
	TokenType   string             `json:"token_type"`
	UserToken   core.UserTokenView `json:"user_token"`
}

func (h *Handler) pollDevice(w http.ResponseWriter, r *http.Request) error {
	if err := h.throttle(r); err != nil {
		return err
	}
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.DeviceCode == "" {
		return badRequest("parameter_missing", "device_code", "Send the device_code from POST /v1/auth/device.")
	}
	plain, t, err := h.svc.PollDevice(r.Context(), body.DeviceCode)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	ok(w, http.StatusOK, issuedToken{Object: "user_token", AccessToken: plain, TokenType: "bearer", UserToken: core.ViewUserToken(t)})
	return nil
}

func (h *Handler) revokeOwnToken(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	if err := h.svc.RevokeOwnToken(r.Context(), a); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.UserToken, *a.TokenID), Object: "user_token", Deleted: true})
	return nil
}
