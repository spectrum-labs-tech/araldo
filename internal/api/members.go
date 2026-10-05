// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/spectrum-labs-tech/araldo/internal/apperr"
	"github.com/spectrum-labs-tech/araldo/internal/core"
	"github.com/spectrum-labs-tech/araldo/internal/id"
	"github.com/spectrum-labs-tech/araldo/internal/model"
)

// The org and its members (ADR 0028): for a person, through a user token,
// never an API key (ADR 0019). Changes that need sudo mode (owners,
// no longer requiring two-factor) stay in the dashboard.

// personOnly refuses an API key.
func personOnly(a core.Actor) error {
	if a.IsKey() {
		return apperr.Forbidden("API keys cannot manage the org or its members: sign in as yourself with araldo auth login.")
	}
	return nil
}

func (h *Handler) getOrg(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	if err := personOnly(a); err != nil {
		return err
	}
	o, err := h.svc.Org(r.Context(), a)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewOrg(o))
	return nil
}

func (h *Handler) updateOrg(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	if err := personOnly(a); err != nil {
		return err
	}
	var body struct {
		Name       *string `json:"name"`
		RequireMFA *bool   `json:"require_mfa"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	o, err := h.svc.Org(r.Context(), a)
	if err != nil {
		return err
	}
	name, mfa := o.Name, o.RequireMFA
	if body.Name != nil {
		name = *body.Name
	}
	if body.RequireMFA != nil {
		mfa = *body.RequireMFA
	}
	if err := h.svc.UpdateOrg(r.Context(), a, nil, name, mfa); err != nil {
		return err
	}
	if o, err = h.svc.Org(r.Context(), a); err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewOrg(o))
	return nil
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) error {
	ms, err := h.svc.Members(r.Context(), actor(r))
	if err != nil {
		return err
	}
	out := make([]core.MemberView, 0, len(ms))
	for i := range ms {
		out = append(out, core.ViewMember(&ms[i]))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/members"})
	return nil
}

func (h *Handler) getMember(w http.ResponseWriter, r *http.Request) error {
	uid, err := pathID(r, id.User, "member")
	if err != nil {
		return err
	}
	m, err := h.svc.Member(r.Context(), actor(r), uid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewMember(m))
	return nil
}

func (h *Handler) updateMember(w http.ResponseWriter, r *http.Request) error {
	uid, err := pathID(r, id.User, "member")
	if err != nil {
		return err
	}
	var body struct {
		Role model.Role `json:"role"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	a := actor(r)
	if err := personOnly(a); err != nil {
		return err
	}
	if err := h.svc.SetMemberRole(r.Context(), a, nil, uid, body.Role); err != nil {
		return err
	}
	m, err := h.svc.Member(r.Context(), a, uid)
	if err != nil {
		return err
	}
	ok(w, http.StatusOK, core.ViewMember(m))
	return nil
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) error {
	uid, err := pathID(r, id.User, "member")
	if err != nil {
		return err
	}
	a := actor(r)
	if err := personOnly(a); err != nil {
		return err
	}
	if err := h.svc.RemoveMember(r.Context(), a, nil, uid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.User, uid), Object: "member", Deleted: true})
	return nil
}

func (h *Handler) listInvitations(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	if err := personOnly(a); err != nil {
		return err
	}
	invs, err := h.svc.Invitations(r.Context(), a)
	if err != nil {
		return err
	}
	out := make([]core.InvitationView, 0, len(invs))
	for _, inv := range invs {
		out = append(out, core.ViewInvitation(inv))
	}
	ok(w, http.StatusOK, list{Object: "list", Data: out, URL: "/v1/invitations"})
	return nil
}

func (h *Handler) createInvitation(w http.ResponseWriter, r *http.Request) error {
	a := actor(r)
	if err := personOnly(a); err != nil {
		return err
	}
	var body struct {
		Email string     `json:"email"`
		Role  model.Role `json:"role"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	link, inv, err := h.svc.InviteMember(r.Context(), a, nil, body.Email, body.Role)
	if err != nil {
		return err
	}
	v := core.ViewInvitation(inv)
	v.URL = link
	ok(w, http.StatusCreated, v)
	return nil
}

func (h *Handler) revokeInvitation(w http.ResponseWriter, r *http.Request) error {
	iid, err := pathID(r, id.Invitation, "invitation")
	if err != nil {
		return err
	}
	a := actor(r)
	if err := personOnly(a); err != nil {
		return err
	}
	if err := h.svc.RevokeInvitation(r.Context(), a, iid); err != nil {
		return err
	}
	ok(w, http.StatusOK, deleted{ID: id.Format(id.Invitation, iid), Object: "invitation", Deleted: true})
	return nil
}
