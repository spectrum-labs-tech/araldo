// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/spectrum-labs-tech/araldo/internal/ads"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
)

// Ads (ADR 0023).

func (h *Handler) listAdNetworks(w http.ResponseWriter, r *http.Request) error {
	networks := h.svc.AdNetworks(actor(r).Livemode)
	out := make([]core.AdNetworkView, 0, len(networks))
	for _, n := range networks {
		out = append(out, core.ViewAdNetwork(n))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/ad_networks"})
	return nil
}

func (h *Handler) listAdAccounts(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var brandID *uuid.UUID
	if ref := r.URL.Query().Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		brandID = &b.ID
	}
	accts, err := h.svc.AdAccounts(r.Context(), a, brandID)
	if err != nil {
		return err
	}
	out := make([]core.AdAccountView, 0, len(accts))
	for _, ac := range accts {
		out = append(out, core.ViewAdAccount(ac))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/ad_accounts"})
	return nil
}

func (h *Handler) createAdAccount(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	var body struct {
		Brand   string            `json:"brand"`
		Network string            `json:"network"`
		Fields  map[string]string `json:"fields"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	b, err := h.svc.ResolveBrand(r.Context(), a, body.Brand)
	if err != nil {
		return err
	}
	ac, err := h.svc.ConnectAdAccount(r.Context(), a, core.AdAccountInput{BrandID: b.ID, Network: ads.Network(body.Network), Fields: body.Fields})
	if err != nil {
		return err
	}
	ok(w, http.StatusCreated, core.ViewAdAccount(ac))
	return nil
}

func (h *Handler) getAdAccount(w http.ResponseWriter, r *http.Request) error {
	aid, err := pathID(r, id.AdAccount, "ad account")
	if err != nil {
		return err
	}
	ac, err := h.svc.AdAccount(r.Context(), actor(r), aid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewAdAccount(ac))
	return nil
}

func (h *Handler) deleteAdAccount(w http.ResponseWriter, r *http.Request) error {
	aid, err := pathID(r, id.AdAccount, "ad account")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteAdAccount(r.Context(), actor(r), aid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.AdAccount, aid), Object: "ad_account", Deleted: true})
	return nil
}

func (h *Handler) adsSummary(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	q := r.URL.Query()
	f := core.AdsFilter{GroupBy: core.AdsGroup(q.Get("group_by"))}
	if err := window(q, time.DateOnly, &f.Since, &f.Until); err != nil {
		return err
	}
	var err error
	if f.Limit, err = limitParam(q); err != nil {
		return err
	}
	if ref := q.Get("brand"); ref != "" {
		b, err := h.svc.ResolveBrand(r.Context(), a, ref)
		if err != nil {
			return err
		}
		f.BrandID = &b.ID
	}
	if v := q.Get("account"); v != "" {
		u, err := id.Parse(id.AdAccount, v)
		if err != nil {
			return badRequest("parameter_invalid", "account", "account must be an ad account ID.")
		}
		f.AccountID = &u
	}
	sum, err := h.svc.AdsSummary(r.Context(), a, f)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewAdsSummary(sum))
	return nil
}
