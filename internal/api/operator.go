// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"time"

	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The operator API (ADR 0031), under /v1/operator/: called with operator
// keys only, by the install's own service, to create and manage orgs.

func (h *Handler) operatorRoutes() {
	h.operator("GET /v1/operator/orgs", h.listOperatedOrgs, paged("external_ref")...)
	h.operator("POST /v1/operator/orgs", h.createOperatedOrg)
	h.operator("GET /v1/operator/orgs/{id}", h.getOperatedOrg)
	h.operator("POST /v1/operator/orgs/{id}", h.changeOperatedOrg)
	h.operator("DELETE /v1/operator/orgs/{id}", h.deleteOperatedOrg, "confirm")
	h.operator("POST /v1/operator/orgs/{id}/invitations", h.operatorInvite)
	h.operator("GET /v1/operator/orgs/{id}/usage", h.operatedOrgUsage, "month")
}

// operator registers a route for operator keys only.
func (h *Handler) operator(pattern string, fn handlerFunc, query ...string) {
	h.handle(pattern, fn, query...)
	h.operatorOnly[pattern] = true
}

// inOrg is the operator acting in the org the path names.
func (h *Handler) inOrg(r *http.Request) (core.Actor, *model.Org, error) {
	orgID, err := pathID(r, id.Org, "org")
	if err != nil {
		return core.Actor{}, nil, err
	}
	return h.svc.InOrg(r.Context(), actor(r), orgID)
}

func (h *Handler) listOperatedOrgs(w http.ResponseWriter, r *http.Request) error {
	pg, err := page(r, id.Org)
	if err != nil {
		return err
	}
	orgs, more, err := h.svc.OperatedOrgs(r.Context(), actor(r), pg, r.URL.Query().Get("external_ref"))
	if err != nil {
		return err
	}
	out := make([]core.OperatedOrgView, 0, len(orgs))
	for _, o := range orgs {
		out = append(out, core.ViewOperatedOrg(o))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, HasMore: more, URL: "/v1/operator/orgs"})
	return nil
}

func (h *Handler) createOperatedOrg(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name        string          `json:"name"`
		OwnerEmail  string          `json:"owner_email"`
		ExternalRef string          `json:"external_ref"`
		Limits      model.OrgLimits `json:"limits"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	o, link, inv, err := h.svc.CreateOperatedOrg(r.Context(), actor(r), core.OperatedOrgInput{Name: body.Name, OwnerEmail: body.OwnerEmail,
		ExternalRef: body.ExternalRef, Limits: body.Limits})
	if err != nil {
		return err
	}
	v := core.ViewOperatedOrg(o)
	iv := core.ViewInvitation(inv)
	iv.URL = link
	v.OwnerInvitation = &iv
	ok(w, http.StatusCreated, v)
	return nil
}

func (h *Handler) getOperatedOrg(w http.ResponseWriter, r *http.Request) error {
	_, o, err := h.inOrg(r)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewOperatedOrg(o))
	return nil
}

func (h *Handler) changeOperatedOrg(w http.ResponseWriter, r *http.Request) error {
	a, _, err := h.inOrg(r)
	if err != nil {
		return err
	}
	var body struct {
		Name        *string          `json:"name"`
		Status      *model.OrgStatus `json:"status"`
		StatusNote  *string          `json:"status_note"`
		ExternalRef *string          `json:"external_ref"`
		Limits      *model.OrgLimits `json:"limits"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	o, err := h.svc.ChangeOrg(r.Context(), a, core.OrgChange{Name: body.Name, Status: body.Status, StatusNote: body.StatusNote,
		ExternalRef: body.ExternalRef, Limits: body.Limits})
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewOperatedOrg(o))
	return nil
}

func (h *Handler) deleteOperatedOrg(w http.ResponseWriter, r *http.Request) error {
	a, o, err := h.inOrg(r)
	if err != nil {
		return err
	}
	if err := h.svc.DeleteOrg(r.Context(), a, nil, r.URL.Query().Get("confirm")); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.Org, o.ID), Object: "org", Deleted: true})
	return nil
}

func (h *Handler) operatorInvite(w http.ResponseWriter, r *http.Request) error {
	a, _, err := h.inOrg(r)
	if err != nil {
		return err
	}
	var body struct {
		Email string     `json:"email"`
		Role  model.Role `json:"role"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	link, inv, err := h.svc.InviteAsOperator(r.Context(), a, body.Email, body.Role)
	if err != nil {
		return err
	}
	v := core.ViewInvitation(inv)
	v.URL = link
	ok(w, http.StatusCreated, v)
	return nil
}

func (h *Handler) operatedOrgUsage(w http.ResponseWriter, r *http.Request) error {
	a, o, err := h.inOrg(r)
	if err != nil {
		return err
	}
	at := time.Now()
	if m := r.URL.Query().Get("month"); m != "" {
		if at, err = time.Parse("2006-01", m); err != nil {
			return badRequest("parameter_invalid", "month", "month is a calendar month, such as 2026-10.")
		}
	}
	u, err := h.svc.Usage(r.Context(), a, at)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewUsage(o.ID, u))
	return nil
}
